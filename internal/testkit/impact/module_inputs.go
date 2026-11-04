package impact

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type goModule struct {
	Path, Dir, GoMod, Version string
	Main                      bool
	Replace                   *goModule
}

type buildInputs struct {
	overlay          compilerOverlay
	flags, workspace string
}

// File-valued module configuration is mutable independently of its flag text.
func moduleConfigIdentity(dir, flags, workspace string) (string, error) {
	words, err := goFlagWords(flags)
	if err != nil {
		return "", err
	}
	module := ""
	for _, word := range words {
		if value, ok := strings.CutPrefix(word, "-modfile="); ok {
			module = value
		}
	}
	files := map[string]bool{}
	if module != "" {
		module = buildPath(dir, module)
		files[module] = false
		files[strings.TrimSuffix(module, ".mod")+".sum"] = true
	}
	if workspace != "" && workspace != "off" {
		workspace = buildPath(dir, workspace)
		files[workspace], files[workspace+".sum"] = false, true
	}
	if len(files) == 0 {
		return "", nil
	}
	var paths []string
	for path := range files {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	var identity []string
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) && files[path] {
			identity = append(identity, path, "missing")
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read effective module configuration: %w", err)
		}
		identity = append(identity, path, hash(data))
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	return hash(data), nil
}

type localModuleInputs map[string]string

func (inputs localModuleInputs) capture(dir string, files []string, module *goModule, main string) (bool, error) {
	if module == nil || module.Path == main || (!module.Main && (module.Replace == nil || module.Replace.Version != "")) {
		return false, nil
	}
	for _, file := range files {
		path := buildPath(dir, file)
		data, err := os.ReadFile(path)
		if err != nil {
			return true, fmt.Errorf("read mutable module build input: %w", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return true, fmt.Errorf("stat mutable module build input: %w", err)
		}
		inputs[path] = hash(fmt.Appendf(nil, "%d:%s", info.Mode()&0o111, hash(data)))
	}
	if module.Replace != nil {
		module = module.Replace
	}
	mod := module.GoMod
	if mod == "" {
		mod = filepath.Join(module.Dir, "go.mod")
	}
	data, err := os.ReadFile(mod)
	if err != nil {
		return true, fmt.Errorf("read mutable module manifest: %w", err)
	}
	inputs[mod] = hash(data)
	return true, nil
}

func (inputs localModuleInputs) digest() (string, error) {
	if len(inputs) == 0 {
		return "", nil
	}
	data, err := json.Marshal(inputs)
	if err != nil {
		return "", err
	}
	return hash(data), nil
}
