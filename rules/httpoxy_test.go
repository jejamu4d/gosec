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
	"testing"

	gas "github.com/HewlettPackard/gas/core"
)

func TestHttpoxy(t *testing.T) {
	analyzer := gas.NewAnalyzer(false, nil, nil)
	analyzer.AddRule(NewHttpoxyTest())

	issues := gasTestRunner(`
		package main
	        import (
                	"log"
                	"net/http/cgi"
        	)
		func main() {
		}`, analyzer)

	checkTestResults(t, issues, 1, "Go code running under CGI is vulnerable to Httpoxy attack.")
}
