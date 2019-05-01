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

// Package gosec holds the central scanning logic used by gosec security scanner
package gosec

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/token"
	"go/types"
	"log"
	"os"
	"path"
	"reflect"
	"regexp"
	"strconv"

	"strings"

	"golang.org/x/tools/go/packages"
)

// The Context is populated with data parsed from the source code as it is scanned.
// It is passed through to all rule functions as they are called. Rules may use
// this data in conjunction withe the encountered AST node.
type Context struct {
	FileSet  *token.FileSet
	Comments ast.CommentMap
	Info     *types.Info
	Pkg      *types.Package
	PkgFiles []*ast.File
	Root     *ast.File
	Config   Config
	Imports  *ImportTracker
	Ignores  []map[string]bool
}

// Metrics used when reporting information about a scanning run.
type Metrics struct {
	NumFiles int `json:"files"`
	NumLines int `json:"lines"`
	NumNosec int `json:"nosec"`
	NumFound int `json:"found"`
}

// Analyzer object is the main object of gosec. It has methods traverse an AST
// and invoke the correct checking rules as on each node as required.
type Analyzer struct {
	ignoreNosec bool
	ruleset     RuleSet
	context     *Context
	config      Config
	logger      *log.Logger
	issues      []*Issue
	stats       *Metrics
	errors      map[string][]Error // keys are file paths; values are the golang errors in those files
	tests       bool
}

// NewAnalyzer builds a new analyzer.
func NewAnalyzer(conf Config, tests bool, logger *log.Logger) *Analyzer {
	ignoreNoSec := false
	if enabled, err := conf.IsGlobalEnabled(Nosec); err == nil {
		ignoreNoSec = enabled
	}
	if logger == nil {
		logger = log.New(os.Stderr, "[gosec]", log.LstdFlags)
	}
	return &Analyzer{
		ignoreNosec: ignoreNoSec,
		ruleset:     make(RuleSet),
		context:     &Context{},
		config:      conf,
		logger:      logger,
		issues:      make([]*Issue, 0, 16),
		stats:       &Metrics{},
		errors:      make(map[string][]Error),
		tests:       tests,
	}
}

// LoadRules instantiates all the rules to be used when analyzing source
// packages
func (gosec *Analyzer) LoadRules(ruleDefinitions map[string]RuleBuilder) {
	for id, def := range ruleDefinitions {
		r, nodes := def(id, gosec.config)
		gosec.ruleset.Register(r, nodes...)
	}
}

// Process kicks off the analysis process for a given package
func (gosec *Analyzer) Process(buildTags []string, packagePaths ...string) error {
	config := gosec.pkgConfig(buildTags)
	for _, pkgPath := range packagePaths {
		pkgs, err := gosec.load(pkgPath, config)
		if err != nil {
			return fmt.Errorf("loading pkg dir %q: %v", pkgPath, err)
		}
		for _, pkg := range pkgs {
			if pkg.Name != "" {
				err := gosec.parseErrors(pkg)
				if err != nil {
					return fmt.Errorf("parsing errors in pkg %q: %v", pkg.Name, err)
				}
				gosec.check(pkg)
			}
		}
	}
	sortErrors(gosec.errors)
	return nil
}

func (gosec *Analyzer) pkgConfig(buildTags []string) *packages.Config {
	tagsFlag := "-tags=" + strings.Join(buildTags, " ")
	return &packages.Config{
		Mode:       packages.LoadSyntax,
		BuildFlags: []string{tagsFlag},
		Tests:      gosec.tests,
	}
}

func (gosec *Analyzer) load(pkgPath string, conf *packages.Config) ([]*packages.Package, error) {
	abspath, err := GetPkgAbsPath(pkgPath)
	if err != nil {
		gosec.logger.Printf("Skipping: %s. Path doesn't exist.", abspath)
		return []*packages.Package{}, nil
	}

	gosec.logger.Println("Import directory:", abspath)
	basePackage, err := build.Default.ImportDir(pkgPath, build.ImportComment)
	if err != nil {
		return []*packages.Package{}, err
	}

	var packageFiles []string
	for _, filename := range basePackage.GoFiles {
		packageFiles = append(packageFiles, path.Join(pkgPath, filename))
	}

	if gosec.tests {
		testsFiles := []string{}
		testsFiles = append(testsFiles, basePackage.TestGoFiles...)
		testsFiles = append(testsFiles, basePackage.XTestGoFiles...)
		for _, filename := range testsFiles {
			packageFiles = append(packageFiles, path.Join(pkgPath, filename))
		}
	}

	pkgs, err := packages.Load(conf, packageFiles...)
	if err != nil {
		return []*packages.Package{}, err
	}
	return pkgs, nil
}

func (gosec *Analyzer) check(pkg *packages.Package) {
	gosec.logger.Println("Checking package:", pkg.Name)
	for _, file := range pkg.Syntax {
		gosec.logger.Println("Checking file:", pkg.Fset.File(file.Pos()).Name())
		gosec.context.FileSet = pkg.Fset
		gosec.context.Config = gosec.config
		gosec.context.Comments = ast.NewCommentMap(gosec.context.FileSet, file, file.Comments)
		gosec.context.Root = file
		gosec.context.Info = pkg.TypesInfo
		gosec.context.Pkg = pkg.Types
		gosec.context.PkgFiles = pkg.Syntax
		gosec.context.Imports = NewImportTracker()
		gosec.context.Imports.TrackFile(file)
		ast.Walk(gosec, file)
		gosec.stats.NumFiles++
		gosec.stats.NumLines += pkg.Fset.File(file.Pos()).LineCount()
	}
}

func (gosec *Analyzer) parseErrors(pkg *packages.Package) error {
	if len(pkg.Errors) == 0 {
		return nil
	}
	for _, pkgErr := range pkg.Errors {
		// infoErr contains information about the error
		// at index 0 is the file path
		// at index 1 is the line; index 2 is for column
		// at index 3 is the actual error
		infoErr := strings.Split(pkgErr.Error(), ":")
		filePath := infoErr[0]
		var err error
		var line, column int
		var errorMsg string
		if len(infoErr) == 4 {
			if line, err = strconv.Atoi(infoErr[1]); err != nil {
				return fmt.Errorf("parsing line: %v", err)
			}
			if column, err = strconv.Atoi(infoErr[2]); err != nil {
				return fmt.Errorf("parsing column: %v", err)
			}
			errorMsg = strings.TrimSpace(infoErr[3])
		} else if len(infoErr) > 1 {
			errorMsg = strings.TrimSpace(infoErr[1])
		} else {
			return fmt.Errorf("cannot parse error %q", infoErr)
		}
		newErr := NewError(line, column, errorMsg)
		if errSlice, ok := gosec.errors[filePath]; ok {
			gosec.errors[filePath] = append(errSlice, *newErr)
		} else {
			errSlice = []Error{}
			gosec.errors[filePath] = append(errSlice, *newErr)
		}
	}
	return nil
}

// ignore a node (and sub-tree) if it is tagged with a "#nosec" comment
func (gosec *Analyzer) ignore(n ast.Node) ([]string, bool) {
	if groups, ok := gosec.context.Comments[n]; ok && !gosec.ignoreNosec {
		for _, group := range groups {
			if strings.Contains(group.Text(), "#nosec") {
				gosec.stats.NumNosec++

				// Pull out the specific rules that are listed to be ignored.
				re := regexp.MustCompile(`(G\d{3})`)
				matches := re.FindAllStringSubmatch(group.Text(), -1)

				// If no specific rules were given, ignore everything.
				if len(matches) == 0 {
					return nil, true
				}

				// Find the rule IDs to ignore.
				var ignores []string
				for _, v := range matches {
					ignores = append(ignores, v[1])
				}
				return ignores, false
			}
		}
	}
	return nil, false
}

// Visit runs the gosec visitor logic over an AST created by parsing go code.
// Rule methods added with AddRule will be invoked as necessary.
func (gosec *Analyzer) Visit(n ast.Node) ast.Visitor {
	// If we've reached the end of this branch, pop off the ignores stack.
	if n == nil {
		if len(gosec.context.Ignores) > 0 {
			gosec.context.Ignores = gosec.context.Ignores[1:]
		}
		return gosec
	}

	// Get any new rule exclusions.
	ignoredRules, ignoreAll := gosec.ignore(n)
	if ignoreAll {
		return nil
	}

	// Now create the union of exclusions.
	ignores := map[string]bool{}
	if len(gosec.context.Ignores) > 0 {
		for k, v := range gosec.context.Ignores[0] {
			ignores[k] = v
		}
	}

	for _, v := range ignoredRules {
		ignores[v] = true
	}

	// Push the new set onto the stack.
	gosec.context.Ignores = append([]map[string]bool{ignores}, gosec.context.Ignores...)

	// Track aliased and initialization imports
	gosec.context.Imports.TrackImport(n)

	for _, rule := range gosec.ruleset.RegisteredFor(n) {
		if _, ok := ignores[rule.ID()]; ok {
			continue
		}
		issue, err := rule.Match(n, gosec.context)
		if err != nil {
			file, line := GetLocation(n, gosec.context)
			file = path.Base(file)
			gosec.logger.Printf("Rule error: %v => %s (%s:%d)\n", reflect.TypeOf(rule), err, file, line)
		}
		if issue != nil {
			gosec.issues = append(gosec.issues, issue)
			gosec.stats.NumFound++
		}
	}
	return gosec
}

// Report returns the current issues discovered and the metrics about the scan
func (gosec *Analyzer) Report() ([]*Issue, *Metrics, map[string][]Error) {
	return gosec.issues, gosec.stats, gosec.errors
}

// Reset clears state such as context, issues and metrics from the configured analyzer
func (gosec *Analyzer) Reset() {
	gosec.context = &Context{}
	gosec.issues = make([]*Issue, 0, 16)
	gosec.stats = &Metrics{}
}
