// (c) Copyright 2016 Hewlett Packard Enterprise Development LP
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rules

import (
	"go/ast"
	"go/types"

	"github.com/GoASTScanner/gas"
)

type readfile struct {
	gas.CallList
}

// Match inspects AST nodes to determine if the match the methods `os.Open` or `ioutil.ReadFile`
func (r *readfile) Match(n ast.Node, c *gas.Context) (*gas.Issue, error) {
	if node := r.ContainsCallExpr(n, c); node != nil {
		for _, arg := range node.Args {
			if ident, ok := arg.(*ast.Ident); ok {
				obj := c.Info.ObjectOf(ident)
				if _, ok := obj.(*types.Var); ok && !gas.TryResolve(ident, c) {
					return gas.NewIssue(c, n, "File inclusion launched with variable", gas.Medium, gas.High), nil
				}
			}
		}
	}
	return nil, nil
}

// NewReadFile detects cases where we read files
func NewReadFile(conf gas.Config) (gas.Rule, []ast.Node) {
	rule := &readfile{gas.NewCallList()}
	rule.Add("io/ioutil", "ReadFile")
	rule.Add("os", "Open")
	return rule, []ast.Node{(*ast.CallExpr)(nil)}
}
