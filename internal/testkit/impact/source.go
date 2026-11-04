package impact

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/doc"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func isRoot(name string) bool {
	if name == "TestMain" {
		return false
	}
	for _, prefix := range []string{"Test", "Example", "Fuzz"} {
		if strings.HasPrefix(name, prefix) {
			rest := strings.TrimPrefix(name, prefix)
			if rest == "" {
				return true
			}
			r, _ := utf8.DecodeRuneInString(rest)
			return !unicode.IsLower(r)
		}
	}
	return false
}

func inspectionImport(name string) bool {
	if strings.HasPrefix(name, "go/") || name == "golang.org/x/tools/go/packages" {
		return true
	}
	switch name {
	case "os", "io/fs", "io/ioutil", "path/filepath", "syscall", "golang.org/x/sys/unix", "C":
		return true
	}
	return false
}

func parseFile(path, pkg string, data []byte) (File, error) {
	f := File{Digest: hash(data), Package: pkg, Functions: map[string]Function{}}
	if !strings.HasSuffix(path, ".go") {
		switch filepath.Ext(path) {
		case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".mjs", ".cjs":
			// These module identifiers also cover require(), dynamic imports and
			// getBuiltinModule(). False positives only widen the Web workload.
			f.SourceInspection = regexp.MustCompile(`["'\x60](?:node:)?(?:fs(?:/promises)?|child_process|worker_threads|module)["'\x60]`).Match(data)
		}
		return f, nil
	}
	fs := token.NewFileSet()
	a, err := parser.ParseFile(fs, path, data, parser.ParseComments)
	if err != nil {
		return f, err
	}
	// File APIs can consume module resources without an import edge to their
	// owner. An os.Root or fs.FS may escape through a helper, so do not limit
	// this boundary to direct ReadFile calls or .go string literals.
	for _, spec := range a.Imports {
		name, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return f, err
		}
		if inspectionImport(name) {
			f.SourceInspection = true
		}
	}
	ast.Inspect(a, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && (sel.Sel.Name == "StartProcess" || sel.Sel.Name == "ForkExec") {
			f.ProcessBoundary = true
		}
		if literal, ok := n.(*ast.BasicLit); ok && literal.Kind == token.STRING {
			if value, err := strconv.Unquote(literal.Value); err == nil && strings.HasSuffix(value, ".go") {
				f.SourceInspection = true
			}
		}
		return true
	})
	examples := map[string]bool{}
	if strings.HasSuffix(path, "_test.go") {
		for _, example := range doc.Examples(a) {
			if example.Output != "" || example.EmptyOutput {
				examples["Example"+example.Name] = true
			}
		}
	}
	var header bytes.Buffer
	fmt.Fprintln(&header, a.Name.Name)
	for _, cg := range a.Comments {
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//go:") {
				fmt.Fprintln(&header, c.Text)
			}
		}
	}
	for _, d := range a.Decls {
		if declaration, ok := d.(*ast.GenDecl); ok && declaration.Tok == token.VAR {
			f.SharedState = true
		}
		if fd, ok := d.(*ast.FuncDecl); ok {
			name := fd.Name.Name
			if fd.Recv != nil {
				var b bytes.Buffer
				if err := format.Node(&b, fs, fd.Recv.List[0].Type); err != nil {
					return f, err
				}
				name = b.String() + "." + name
			}
			id := pkg + ":" + name
			if fd.Name.Name == "init" {
				id += ":" + path + fmt.Sprint(fs.Position(fd.Pos()).Line)
			}
			var body bytes.Buffer
			if fd.Body != nil {
				// Example output directives are comments inside the body. Hash the
				// complete body text so changing an expected output cannot disappear
				// when formatting a standalone AST node without File.Comments.
				body.Write(data[fs.Position(fd.Body.Pos()).Offset:fs.Position(fd.Body.End()).Offset])
			}
			root := strings.HasSuffix(path, "_test.go") && fd.Recv == nil && isRoot(fd.Name.Name)
			if strings.HasPrefix(fd.Name.Name, "Example") && !examples[fd.Name.Name] {
				root = false
			}
			f.Functions[id] = Function{ID: id, Name: fd.Name.Name, Digest: hash(body.Bytes()), Start: fs.Position(fd.Pos()).Line, End: fs.Position(fd.End()).Line, Root: root}
			copy := *fd
			copy.Doc, copy.Body = nil, nil
			if err := format.Node(&header, fs, &copy); err != nil {
				return f, err
			}
		} else {
			if err := format.Node(&header, fs, d); err != nil {
				return f, err
			}
		}
	}
	f.Header = hash(header.Bytes())
	return f, nil
}

// Capture uses Git's ignored-file boundary, including staged/unstaged/untracked
// sources, and Go's active production/test imports for the current build.
func Capture(ctx context.Context, dir, goTool string) (Snapshot, error) {
	s := Snapshot{Files: map[string]File{}, Packages: map[string]Package{}}
	var err error
	dir, err = filepath.Abs(dir)
	if err != nil {
		return s, err
	}
	var inputs buildInputs
	s.Profile, inputs, err = buildProfile(ctx, dir, goTool)
	if err != nil {
		return s, err
	}
	overlay := inputs.overlay
	module, err := output(ctx, dir, goTool, "list", "-m", "-json")
	if err != nil {
		return s, err
	}
	modules := json.NewDecoder(bytes.NewReader(module))
	for {
		var m goModule
		if err := modules.Decode(&m); err == io.EOF {
			break
		} else if err != nil {
			return s, err
		}
		if m.Main && filepath.Clean(m.Dir) == dir {
			s.Module = m.Path
		}
	}
	if s.Module == "" {
		return s, fmt.Errorf("repository directory must be an active Go module root")
	}
	listed, err := output(ctx, dir, goTool, "list", "-race", "-deps", "-json", "./...")
	if err != nil {
		return s, err
	}
	type goPackage struct {
		Module                                           *goModule
		ImportPath, Dir                                  string
		Imports, TestImports, XTestImports               []string
		TestGoFiles, XTestGoFiles                        []string
		GoFiles, CgoFiles                                []string
		EmbedFiles, TestEmbedFiles, XTestEmbedFiles      []string
		CFiles, CXXFiles, MFiles, HFiles, FFiles, SFiles []string
		SwigFiles, SwigCXXFiles, SysoFiles               []string
	}
	var buildInputs []string
	localInputs := localModuleInputs{}
	productionProcess, testProcess := map[string]bool{}, map[string]bool{}
	productionInspection, testInspection := map[string]bool{}, map[string]bool{}
	captureLocal := func(p goPackage) error {
		var files []string
		for _, group := range [][]string{p.GoFiles, p.CgoFiles, p.EmbedFiles, p.CFiles, p.CXXFiles, p.MFiles, p.HFiles, p.FFiles, p.SFiles, p.SwigFiles, p.SwigCXXFiles, p.SysoFiles} {
			files = append(files, group...)
		}
		mutable, err := localInputs.capture(p.Dir, files, p.Module, s.Module)
		if mutable {
			productionInspection[p.ImportPath] = true
		}
		return err
	}
	productionState, testState := map[string]bool{}, map[string]bool{}
	productionImports := map[string][]string{}
	dec := json.NewDecoder(bytes.NewReader(listed))
	for {
		var p goPackage
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return s, err
		}
		productionImports[p.ImportPath] = p.Imports
		if err := captureLocal(p); err != nil {
			return s, err
		}
		if p.ImportPath == "os/exec" {
			productionProcess[p.ImportPath] = true
		}
		if inspectionImport(p.ImportPath) {
			productionInspection[p.ImportPath] = true
		}
		if p.Module == nil || p.Module.Path != s.Module {
			continue
		}
		rel, err := filepath.Rel(dir, p.Dir)
		if err != nil {
			return s, err
		}
		imports := append(slices.Clone(p.TestImports), p.XTestImports...)
		productionProcess[p.ImportPath] = slices.Contains(p.Imports, "os/exec") || slices.Contains(p.Imports, "syscall")
		testProcess[p.ImportPath] = slices.Contains(imports, "os/exec") || slices.Contains(imports, "syscall")
		for _, dep := range p.Imports {
			productionInspection[p.ImportPath] = productionInspection[p.ImportPath] || strings.HasPrefix(dep, "go/") || dep == "golang.org/x/tools/go/packages"
		}
		for _, dep := range imports {
			testInspection[p.ImportPath] = testInspection[p.ImportPath] || strings.HasPrefix(dep, "go/") || dep == "golang.org/x/tools/go/packages"
		}
		entry := Package{ImportPath: p.ImportPath, Dir: filepath.ToSlash(rel), Imports: p.Imports, TestImports: imports}
		// Source discovery is used for dry-run planning. Collection confirms the
		// authoritative runtime inventory, including executable Examples only.
		active := append(slices.Clone(p.GoFiles), p.CgoFiles...)
		active = append(active, p.TestGoFiles...)
		active = append(active, p.XTestGoFiles...)
		for _, file := range append(append(append(slices.Clone(active), p.EmbedFiles...), p.TestEmbedFiles...), p.XTestEmbedFiles...) {
			buildInputs = append(buildInputs, filepath.ToSlash(filepath.Join(rel, file)))
		}
		for _, files := range [][]string{p.CFiles, p.CXXFiles, p.MFiles, p.HFiles, p.FFiles, p.SFiles, p.SwigFiles, p.SwigCXXFiles, p.SysoFiles} {
			for _, file := range files {
				buildInputs = append(buildInputs, filepath.ToSlash(filepath.Join(rel, file)))
			}
		}
		for _, file := range active {
			b, err := overlay.readFile(dir, filepath.Join(p.Dir, file))
			if err != nil {
				return s, err
			}
			parsed, err := parseFile(file, p.ImportPath, b)
			if err != nil {
				return s, err
			}
			if parsed.ProcessBoundary {
				if strings.HasSuffix(file, "_test.go") {
					testProcess[p.ImportPath] = true
				} else {
					productionProcess[p.ImportPath] = true
				}
			}
			if parsed.SourceInspection {
				if strings.HasSuffix(file, "_test.go") {
					testInspection[p.ImportPath] = true
				} else {
					productionInspection[p.ImportPath] = true
				}
			}
			if parsed.SharedState {
				if strings.HasSuffix(file, "_test.go") {
					testState[p.ImportPath] = true
				} else {
					productionState[p.ImportPath] = true
				}
			}
			for _, fn := range parsed.Functions {
				if fn.Root {
					entry.Roots = append(entry.Roots, fn.Name)
				}
			}
		}
		slices.Sort(entry.Roots)
		entry.Roots = slices.Compact(entry.Roots)
		s.Packages[p.ImportPath] = entry
	}
	var missing []string
	for _, entry := range s.Packages {
		for _, dep := range entry.TestImports {
			if _, ok := productionImports[dep]; !ok && dep != "testing" {
				missing = append(missing, dep)
			}
		}
	}
	slices.Sort(missing)
	missing = slices.Compact(missing)
	if len(missing) > 0 {
		extra, err := output(ctx, dir, goTool, append([]string{"list", "-race", "-deps", "-json"}, missing...)...)
		if err != nil {
			return s, err
		}
		decoder := json.NewDecoder(bytes.NewReader(extra))
		for {
			var p goPackage
			if err := decoder.Decode(&p); err == io.EOF {
				break
			} else if err != nil {
				return s, err
			}
			productionImports[p.ImportPath] = p.Imports
			if err := captureLocal(p); err != nil {
				return s, err
			}
			if p.ImportPath == "os/exec" {
				productionProcess[p.ImportPath] = true
			}
			if inspectionImport(p.ImportPath) {
				productionInspection[p.ImportPath] = true
			}
		}
	}
	// An exec hidden behind a helper or a production dependency is still a
	// process boundary for the calling test package. Coverage in that child is
	// not claimed as completely attributed by this collector.
	for changed := true; changed; {
		changed = false
		for id, imports := range productionImports {
			for _, dep := range imports {
				// The standard test harness contains the continuous-fuzzing
				// coordinator. Seed execution does not invoke that coordinator.
				if dep == "testing" {
					continue
				}
				if productionProcess[dep] && !productionProcess[id] {
					productionProcess[id] = true
					changed = true
				}
				if productionInspection[dep] && !productionInspection[id] {
					productionInspection[id] = true
					changed = true
				}
				if productionState[dep] && !productionState[id] {
					productionState[id] = true
					changed = true
				}
			}
		}
	}
	for id, p := range s.Packages {
		p.ProcessBoundary = productionProcess[id] || testProcess[id]
		p.SourceInspection = productionInspection[id] || testInspection[id]
		p.SharedState = productionState[id] || testState[id]
		for _, dep := range p.TestImports {
			if dep == "testing" {
				continue
			}
			p.ProcessBoundary = p.ProcessBoundary || productionProcess[dep]
			p.SourceInspection = p.SourceInspection || productionInspection[dep]
			p.SharedState = p.SharedState || productionState[dep]
		}
		s.Packages[id] = p
	}
	files, err := output(ctx, dir, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return s, err
	}
	paths := append(strings.Split(string(files), "\x00"), buildInputs...)
	slices.Sort(paths)
	paths = slices.Compact(paths)
	knownPaths := map[string]bool{}
	for _, path := range paths {
		knownPaths[filepath.Clean(filepath.Join(dir, path))] = true
	}
	var fingerprint bytes.Buffer
	// Go may resolve and write sum files while discovering dependencies. Bind
	// the effective configuration and cleanliness after that resolution.
	s.Profile.ModuleConfig, err = moduleConfigIdentity(dir, inputs.flags, inputs.workspace)
	if err != nil {
		return s, err
	}
	s.Profile.LocalModules, err = localInputs.digest()
	if err != nil {
		return s, err
	}
	if s.Profile.ModuleConfig != "" {
		fmt.Fprintln(&fingerprint, "module-configuration", s.Profile.ModuleConfig)
	}
	if s.Profile.LocalModules != "" {
		fmt.Fprintln(&fingerprint, "local-modules", s.Profile.LocalModules)
	}
	if overlay.digest != "" {
		fmt.Fprintln(&fingerprint, "compiler-overlay", overlay.digest)
	}
	for _, path := range paths {
		if path == "" {
			continue
		}
		info, err := os.Lstat(filepath.Join(dir, path))
		if os.IsNotExist(err) {
			fmt.Fprintln(&fingerprint, path, "deleted")
			entry, exists := overlay.files[buildPath(dir, path)]
			if !exists || entry.deleted {
				continue
			}
		} else if err != nil {
			return s, err
		}
		var b []byte
		var linked []byte
		var mode os.FileMode
		if info == nil {
			// The overlay introduces a virtual file. Its bytes are bound by the
			// overlay digest, while the absent physical file remains explicit.
		} else if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(filepath.Join(dir, path))
			if err != nil {
				return s, err
			}
			b = []byte("symlink:" + link)
			target, resolveErr := filepath.EvalSymlinks(filepath.Join(dir, path))
			var targetInfo os.FileInfo
			if resolveErr == nil {
				targetInfo, resolveErr = os.Stat(target)
			}
			if resolveErr != nil || !targetInfo.Mode().IsRegular() {
				// An unresolved link or directory can expose arbitrary resources.
				// Ordinary full checks still run; precise baselines are refused.
				s.UnboundResources = true
			} else {
				linked, err = os.ReadFile(target)
				if err != nil {
					return s, fmt.Errorf("read linked resource: %w", err)
				}
				if !knownPaths[filepath.Clean(target)] {
					// Tracked targets already contribute their physical bytes. Keep
					// that compatible identity while binding ignored/external bytes.
					fmt.Fprintln(&fingerprint, "linked-resource", path, targetInfo.Mode()&0o111, hash(linked))
					b = fmt.Appendf(b, "\n%d:%s", targetInfo.Mode()&0o111, hash(linked))
				}
			}
		} else if info.Mode().IsRegular() {
			b, err = os.ReadFile(filepath.Join(dir, path))
			if err != nil {
				return s, err
			}
		} else {
			return s, fmt.Errorf("unsupported source entry %s", path)
		}
		if info != nil {
			mode = info.Mode() & 0o111
			// Keep physical source identity even when the compiler overlays it:
			// tests can still read the original bytes at runtime.
			fmt.Fprintln(&fingerprint, path, mode, hash(b))
		}
		pkg := ""
		for id, p := range s.Packages {
			if p.Dir == filepath.ToSlash(filepath.Dir(path)) {
				pkg = id
				break
			}
		}
		digest := hash(b)
		if linked != nil {
			b = linked
		}
		if entry, exists := overlay.files[buildPath(dir, path)]; exists {
			if entry.deleted {
				s.Files[path] = File{Digest: hash(b), Package: pkg, Executable: uint32(mode), Functions: map[string]Function{}}
				continue
			}
			b = entry.data
		}
		f, err := parseFile(path, pkg, b)
		if err != nil {
			return s, err
		}
		f.Executable = uint32(mode)
		if info != nil && info.Mode()&os.ModeSymlink != 0 {
			f.Digest = digest
		}
		s.Files[path] = f
	}
	s.Fingerprint = hash(fingerprint.Bytes())
	commit, err := output(ctx, dir, "git", "rev-parse", "HEAD")
	if err != nil {
		return s, err
	}
	s.Commit = strings.TrimSpace(string(commit))
	status, err := output(ctx, dir, "git", "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return s, err
	}
	s.Clean = len(status) == 0
	return s, nil
}

func SameSource(ctx context.Context, dir, goTool, fingerprint string) error {
	s, err := Capture(ctx, dir, goTool)
	if err != nil {
		return err
	}
	if s.Fingerprint != fingerprint {
		return fmt.Errorf("source changed during validation")
	}
	return nil
}

func SameSnapshot(ctx context.Context, dir, goTool string, expected Snapshot) error {
	s, err := Capture(ctx, dir, goTool)
	if err != nil {
		return err
	}
	if s.Fingerprint != expected.Fingerprint {
		return fmt.Errorf("source changed during validation")
	}
	if s.Profile != expected.Profile {
		return ErrConfigurationChanged
	}
	return nil
}

func SamePlan(ctx context.Context, dir, goTool string, p Plan) error {
	return SameSnapshot(ctx, dir, goTool, Snapshot{Fingerprint: p.Fingerprint, Profile: p.Profile})
}
