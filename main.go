package main

import (
	"fmt"
	"go/build"
	"go/parser"
	"os"
	"strings"

	"golang.org/x/tools/astutil"
	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/loader"
	"golang.org/x/tools/go/ssa"
)

func main() {
	err := doCallGraph(os.Args[1])
	if err != nil {
		panic(err)
	}
}

func doCallGraph(arg string) error {
	conf := loader.Config{
		Build:         &build.Default,
		SourceImports: true,
		ParserMode:    parser.ParseComments,
	}

	// Use the initial packages from the command line.
	_, err := conf.FromArgs([]string{arg}, true)
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
	pkg := iprog.Imported[arg]
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

	cg := cha.CallGraph(prog)

	cg.DeleteSyntheticNodes()
	for k, fn := range roots {
		for _, check := range checks[k] {
			walk(cg, check, fn)
		}
	}

	return nil
}

func walk(cg *callgraph.Graph, leaf *ssa.Function, root *ssa.Function) bool {
	lnode := cg.Nodes[leaf]
	rnode := cg.Nodes[root]
	stack := make([]*callgraph.Edge, 0)
	seen := make(map[*callgraph.Node]bool)
	var search func(n *callgraph.Node) bool
	search = func(n *callgraph.Node) bool {
		if seen[n] {
			return false
		}
		seen[n] = true
		if n == rnode {
			return false
		}
		seenall := true
		for _, e := range n.In {
			if _, ok := e.Site.(*ssa.Go); ok {
				seen[e.Caller] = true
				if !hasRoot(stack, rnode) {
					fmt.Println("trace found with bad root")
					fmt.Println(e.Callee)
					for _, s := range stack {
						fmt.Println(s.Callee)
					}
				}
				return false
			}
			stack = append(stack, e) // push
			if !seen[e.Caller] {
				search(e.Caller)
				seenall = false
			}
			stack = stack[:len(stack)-1] // pop
		}
		if seenall {
			if !hasRoot(stack, rnode) {
				fmt.Println("trace found with bad root")
				fmt.Println(stack[0].Caller)
				for _, s := range stack {
					fmt.Println(s.Callee)
				}
			}
			return true
		}
		return false
	}
	return search(lnode)
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
