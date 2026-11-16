package runner

import "github.com/GhostFlying/tend/internal/testkit/storefixture"

// These wire/business fixtures need independent current-schema ledgers; they do
// not test migrations. Journal reopen and migration tests retain their real open.
var wireLedgerFixture storefixture.Template
