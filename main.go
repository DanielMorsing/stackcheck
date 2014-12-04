package main

import (
	"flag"
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"strings"

	"golang.org/x/tools/astutil"
	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/loader"
	"golang.org/x/tools/go/pointer"
	"golang.org/x/tools/go/ssa"
)

var testpkg = flag.String("testpkg", "", "package which contains the stackcheck comments")

func main() {
	flag.Parse()
	if *testpkg == "" {
		fmt.Println("must give testpkg")
		return
	}
	err := doCallGraph(flag.Args())
	if err != nil {
		fmt.Println(err)
	}
}

func doCallGraph(arg []string) error {
	conf := loader.Config{
		Build:         &build.Default,
		SourceImports: true,
		ParserMode:    parser.ParseComments,
	}
	if len(arg) == 0 {
		arg = []string{*testpkg}
	}

	// Use the initial packages from the command line.
	_, err := conf.FromArgs(arg, true)
	if err != nil {
		return err
	}

	// Load, parse and type-check the whole program.
	iprog, err := conf.Load()
	if err != nil {
		return err
	}

	// Create and build SSA-form program representation.
	prog := ssa.Create(iprog, 0)
	prog.BuildAll()

	const stkcheck = "stackcheck: "
	// find all instances of // stackcheck: label
	tpkg := iprog.ImportMap[*testpkg]
	if tpkg == nil {
		return fmt.Errorf("%s not in scope", *testpkg)
	}
	pkg := iprog.AllPackages[tpkg]
	ssapkg := prog.Package(pkg.Pkg)

	roots := make(map[string]*ssa.Function)
	checks := make(map[string][]*ssa.Function)
	for _, f := range pkg.Files {
		for _, c := range f.Comments {
			ctext := c.Text()
			if strings.HasPrefix(ctext, stkcheck) {
				ctext = ctext[len(stkcheck):]
				path, _ := astutil.PathEnclosingInterval(f, c.Pos(), c.End())

				funcd := ssa.EnclosingFunction(ssapkg, path)
				if funcd != nil {
					if strings.HasPrefix(ctext, "root ") {
						ctext = ctext[len("root "):]
						ctext = strings.TrimSpace(ctext)
						roots[ctext] = funcd
					} else {
						ctext = strings.TrimSpace(ctext)
						checks[ctext] = append(checks[ctext], funcd)
					}
				}
			}
		}
	}
	var testPkgs, mains []*ssa.Package
	for _, info := range iprog.InitialPackages() {
		initialPkg := prog.Package(info.Pkg)

		// Add package to the pointer analysis scope.
		if initialPkg.Func("main") != nil {
			mains = append(mains, initialPkg)
		} else {
			testPkgs = append(testPkgs, initialPkg)
		}
	}
	if testPkgs != nil {
		if p := prog.CreateTestMainPackage(testPkgs...); p != nil {
			mains = append(mains, p)
		}
	}

	config := &pointer.Config{
		Mains:          mains,
		BuildCallGraph: true,
	}
	ptares, err := pointer.Analyze(config)
	if err != nil {
		return err // internal error in pointer analysis
	}

	ptares.CallGraph.DeleteSyntheticNodes()
	for k, fn := range roots {
		for _, check := range checks[k] {
			walk(ptares.CallGraph, iprog.Fset, check, fn)
		}
	}

	return nil
}

func walk(cg *callgraph.Graph, fset *token.FileSet, leaf *ssa.Function, root *ssa.Function) {
	lnode := cg.Nodes[leaf]
	rnode := cg.Nodes[root]
	stack := make([]*callgraph.Edge, 0)
	seen := make(map[*callgraph.Node]bool)
	var search func(n *callgraph.Node)
	search = func(n *callgraph.Node) {
		if seen[n] {
			return
		}
		seen[n] = true
		if n == rnode {
			return
		}
		check := []*callgraph.Edge{}
		for _, e := range n.In {
			if _, ok := e.Site.(*ssa.Go); ok {
				continue
			}
			check = append(check, e)
		}
		if len(check) == 0 {
			if !hasRoot(stack, rnode) {
				fmt.Println("trace found with bad root")
				var s *callgraph.Edge
				for _, s = range stack {
					pos := fset.Position(s.Site.Pos())
					fmt.Print("\t", pos, ": ")
					fmt.Println(s.Callee.Func)
				}
				pos := fset.Position(s.Site.Pos())
				fmt.Print("\t", pos, ": ")
				fmt.Println(s.Caller.Func)
			}
			return
		}
		for _, e := range check {
			stack = append(stack, e) // push
			search(e.Caller)
			stack = stack[:len(stack)-1] // pop
		}
	}
	search(lnode)
	return
}

func hasRoot(stack []*callgraph.Edge, rnode *callgraph.Node) bool {
	var s *callgraph.Edge
	var hasnode bool
	for _, s = range stack {
		if s.Caller == rnode {
			hasnode = true
			break
		}
	}
	return hasnode
}
