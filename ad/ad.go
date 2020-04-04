// Package ad implements automatic differentiation of a model.
// A model is defined in it's own package. The model must
// implement interface model.Model. In the model's source code:
//   1. Methods on the type implementing model.Model
//	    returning a single float64 or nothing are
//	    differentiated.
//   2. Within the methods, the following is differentiated:
//      a) assignments to float64 (including parallel
//         assignments if all values are of type float64);
//      b) returns of float64;
//      c) standalone calls to methods on the type implementing
//         model.Model (apparently called for side  effects on
//         the model).
//   3. Imported package name "ad" is reserved.
//   4. Non-dummy identifiers starting with the prefix for
//      generated identifiers ("_" by default) are reserved.
//
// Functions are considered elementals (and must have a
// registered derivative) if their signature is of kind
//         func (float64, float64*) float64
// that is, one or more non-variadic float64 argument and
// float64 return value. For example, function
//         func (float64, float64, float64) float64
// is considered elemental, while functions
//         func (...float64) float64
//         func ([]float64) float64
//         func (int, float64) float64
// are not.
//
// Derivatives do not propagate through a function that is not
// an elemental or a call to a model method. If a derivative is
// not registered for an elemental, calling the elemental in a
// Observe will cause a run-time error.
//
// The differentiated model is put into subpackage "ad" of the
// model's package, with the same name as the original package.
package ad

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
	"log"
	"os"
	"path/filepath"
	"strings"
)

var (
	// When Fold is true, folded values are substituted instead
	// of constant expressions.
	Fold = true
)

const (
	// Import path for the package providing tape functions.
	infergoImport = "bitbucket.org/dtolpin/infergo/ad"
)

// modelInterface is used to identify model types
var modelInterface *types.Interface

func init() {
	modelInterface = types.NewInterface(
		[]*types.Func{
			types.NewFunc(0, nil, "Observe",
				types.NewSignature(nil,
					types.NewTuple(
						types.NewVar(0, nil, "x",
							types.NewSlice(types.Typ[types.Float64]))),
					types.NewTuple(
						types.NewVar(0, nil, "",
							types.Typ[types.Float64])),
					false)),
		},
		nil)
	modelInterface.Complete()
}

// Structure model contains shared data structures for
// differentiating the model. Functions operating on *model are
// defined as method to use shorter names.
type model struct {
	path   string
	fset   *token.FileSet
	pkg    *ast.Package
	info   *types.Info
	prefix string
}

// Deriv differentiates a model. The original model is in the
// package located at mpath. The differentiated model is written
// to mpath/ad. When a variable is generated by the autodiff code,
// the variable name has the specified prefix.
func Deriv(mpath string, prefix string) (err error) {
	// Read the model.
	m := &model{
		prefix: prefix,
	}
	err = m.parse(mpath)
	if err != nil {
		return err
	}

	// Typecheck the model to build the info structure.
	err = m.check()
	if err != nil {
		return err
	}

	// Differentiate the model through rewriting the ASTs
	// in place.
	err = m.deriv()
	if err != nil {
		return err
	}

	// Finally write the model.
	err = m.write()

	return err
}

// Parsing

// parse parses the model's source code and returns the parsed
// package and an error. If the model was parsed successfully,
// the error is nil.
func (m *model) parse(mpath string) (err error) {
	// Read the source code.
	// If there are any errors in the source code, stop.
	m.path = mpath
	m.fset = token.NewFileSet()
	pkgs, err := parser.ParseDir(m.fset, m.path, nil, 0)
	if err != nil { // parse error
		return err
	}

	if len(pkgs) == 0 {
		err = fmt.Errorf("no package in %q", m.path)
		return err
	}

	// There should be a single package, retrieve it. If there
	// is more than a single package, stop.
	for k, v := range pkgs {
		if m.pkg != nil {
			err = fmt.Errorf("multiple packages in %q: %s and %s",
				m.path, m.pkg.Name, k)
			return err
		}
		m.pkg = v
	}

	return err
}

// A types importer aware of modules, instead of the default
// importer which requires installation in the Go path.
type tImporter packages.Config

var importer *tImporter

func init() {
	importer = &tImporter{
		Mode: packages.LoadTypes,
	}
}

func (importer *tImporter) Import(path string) (
	pkg *types.Package,
	err error,
) {
	pkgs, err := packages.Load((*packages.Config)(importer), path)
	return pkgs[0].Types, err
}

// check typechecks the model and builds the info structure.
func (m *model) check() (err error) {
	conf := types.Config{Importer: importer}
	// Check expects the package as a slice of file ASTs.
	var files []*ast.File
	for _, file := range m.pkg.Files {
		files = append(files, file)
	}
	m.info = &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Implicits:  make(map[ast.Node]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}
	_, err = conf.Check(m.path, m.fset, files, m.info)
	return err
}

// Differentiation

// deriv differentiates the model through rewriting the ASTs.
func (m *model) deriv() (err error) {
	// Differentiate each model method.

	// Collect the methods.
	methods, err := m.collectMethods()
	if err != nil {
		return err
	}

	// Simplify the code first so that differentiation
	// is less cumbersome.
	for _, method := range methods {
		err = m.desugar(method)
		if err != nil {
			return err
		}
	}

	// Rewrite file imports, adding the infergo import to all
	// files containing model methods.
	files := make(map[string]bool)
	for _, method := range methods {
		fname := m.fset.Position(method.Pos()).Filename
		processed := files[fname]
		if !processed {
			files[fname] = true
			// Ensure that package name "ad" is available.
			for _, imp := range m.pkg.Files[fname].Imports {
				var o types.Object
				if imp.Name != nil {
					o = m.info.ObjectOf(imp.Name)
				} else {
					o = m.info.Implicits[imp]
				}
				if pkgName, ok := o.(*types.PkgName); ok {
					if pkgName.Name() == "ad" &&
						pkgName.Imported().Path() != infergoImport {
						pos := m.fset.Position(imp.Pos())
						err = fmt.Errorf(
							"%v:%v:%v: package name \"ad\" is reserved",
							pos.Filename, pos.Line, pos.Column)
						return err
					}
				}
			}
			// Add the import (safe to add more than once)
			astutil.AddImport(m.fset, m.pkg.Files[fname], infergoImport)
		}
	}

	// Finally, rewrite the code using tape-writing calls.
	for _, method := range methods {
		err = m.rewrite(method)
		if err != nil {
			return err
		}
	}

	return err
}

// collectMethods collects ASTs of methods defined on the
// models.
func (m *model) collectMethods() (
	methods []*ast.FuncDecl,
	err error,
) {
	// We will mostly have a single model type; a linear
	// lookup is the way to go (see iaType below).
	for _, file := range m.pkg.Files {
		for _, d := range file.Decls {
			if d, ok := d.(*ast.FuncDecl); ok &&
				m.isMethodType(m.info.TypeOf(d.Name)) {
				methods = append(methods, d)
			}
		}
	}

	return methods, err
}

// isMethodType returns true iff typ is a method type of the
// Model interface.
func (m *model) isMethodType(typ types.Type) bool {
	sig, ok := typ.(*types.Signature)
	if !ok {
		return false
	}
	if sig.Recv() == nil || !m.isType(sig.Recv().Type()) {
		return false
	}
	results := sig.Results()
	return results == nil ||
		results.Len() == 0 ||
		results.Len() == 1 && isFloat(results.At(0).Type())
}

// isType returns true iff typ implements the Model interface.
func (m *model) isType(typ types.Type) bool {
	return types.Implements(typ, modelInterface)
}

// errOnPanic turns panic from astutil.Apply into an error,
// for consistent diagnostics.
func errOnPanic(
	err *error,
	pos token.Position,
) func() {
	return func() {
		if r := recover(); r != nil {
			*err = fmt.Errorf("%v:%d:%d: %v",
				pos.Filename, pos.Line, pos.Column, r)
		}
	}
}

// desugar desugars the syntax of a method to differentiate to
// make the automatic differentiation code simpler to write and
// debug.
func (m *model) desugar(method *ast.FuncDecl) (err error) {
	// Apply panics on errors. When Apply panics, we return the
	// error as do other functions.
	defer errOnPanic(
		&err,
		m.fset.Position(method.Pos()),
	)()

	astutil.Apply(method,
		func(c *astutil.Cursor) bool {
			n := c.Node()
			if n != nil && n.Pos() != token.NoPos {
				defer errOnPanic(
					&err,
					m.fset.Position(n.Pos()),
				)()
			}
			switch n := n.(type) {
			case *ast.DeclStmt:
				// If a variable declaration, split into
				// declaration and assignment.
				decl := n.Decl.(*ast.GenDecl)
				if decl.Tok != token.VAR {
					return false
				}
				for ispec, spec := range decl.Specs {
					spec := spec.(*ast.ValueSpec)
					if spec.Values != nil {
						// If a variable declaration assigns
						// values, prune the values and then
						// assign.
						var lhs []ast.Expr
						for _, name := range spec.Names {
							lhs = append(lhs, name)
						}
						asgn := &ast.AssignStmt{
							Lhs:    lhs,
							TokPos: n.Pos(),
							Tok:    token.ASSIGN,
							Rhs:    spec.Values,
						}
						c.InsertAfter(asgn)
						spec.Values = nil
						if spec.Type == nil {
							// Implicit type, we need to make it
							// explicit because we removed the
							// initialization.  Different variables in
							// a single specification with implicit
							// type may have different types.  Split
							// them into separate specifications.
							for i := range spec.Names {
								t := m.info.TypeOf(spec.Names[i])
								typedSpec := &ast.ValueSpec{
									Names: spec.Names[i : i+1],
									Type:  m.typeAst(t, n.Pos()),
								}
								// We override the first
								// specification, and then append the
								// rest of specifications.
								if i == 0 {
									decl.Specs[ispec] = typedSpec
								} else {
									decl.Specs = append(decl.Specs,
										typedSpec)
								}
							}
							if len(decl.Specs) > 1 &&
								decl.Lparen == token.NoPos {
								// The printer needs a non-zero
								// parenthesis position to print
								// multiple specs per decl. This is
								// apparently a bug in Go, pull
								// request 146657 with a fix was
								// submitted.
								decl.Lparen = 1
							}
						}
					}
				}
			case *ast.AssignStmt:
				switch n.Tok {
				case token.ASSIGN:
					// Do nothing, all is well.
				case token.DEFINE:
					// Split into declaration and assignment.
					_, ok := c.Parent().(*ast.BlockStmt)
					if !ok {
						// We can't do it for simple statement,
						// but we do not care either.
						return false
					}

					// Declaration.
					specs := []ast.Spec{}
					for _, l := range n.Lhs {
						ident := l.(*ast.Ident)
						if ident.Name == "_" {
							continue
						}

						o := m.info.ObjectOf(ident)
						if ident.Pos() != o.Pos() {
							// Declared earlier.
							continue
						}

						// Add declaration.
						t := m.info.TypeOf(l)
						specs = append(specs,
							&ast.ValueSpec{
								Names: []*ast.Ident{ident},
								Type:  m.typeAst(t, n.Pos()),
							})
					}
					decl := &ast.GenDecl{
						Tok:   token.VAR,
						Specs: specs}
					if len(decl.Specs) > 1 {
						decl.Lparen = 1
					}
					c.InsertBefore(&ast.DeclStmt{Decl: decl})

					// Just patch the node to get the
					// assignment.
					n.Tok = token.ASSIGN

				case token.ADD_ASSIGN, token.SUB_ASSIGN,
					token.MUL_ASSIGN, token.QUO_ASSIGN:
					// Rewrite as lhs = lhs OP rhs (if lhs is
					// computed with side effects you shoot
					// yourself in the foot).
					tok := map[token.Token]token.Token{
						token.ADD_ASSIGN: token.ADD,
						token.SUB_ASSIGN: token.SUB,
						token.MUL_ASSIGN: token.MUL,
						token.QUO_ASSIGN: token.QUO,
					}[n.Tok]
					n.Tok = token.ASSIGN
					expr := &ast.BinaryExpr{
						X:     n.Lhs[0],
						OpPos: n.Pos(),
						Op:    tok,
						Y:     n.Rhs[0],
					}
					n.Rhs[0] = expr
					// We introduced a new expression after
					// typechecking. Let's add it to the type
					// map.
					m.info.Types[expr] = m.info.Types[n.Lhs[0]]
				}
			case *ast.IncDecStmt:
				// Rewrite as expr = expr OP 1
				one := intExpr(1)
				tok := map[token.Token]token.Token{
					token.INC: token.ADD,
					token.DEC: token.SUB,
				}[n.Tok]
				expr := &ast.BinaryExpr{
					X:     n.X,
					OpPos: n.Pos(),
					Op:    tok,
					Y:     one,
				}
				asgn := &ast.AssignStmt{
					Lhs:    []ast.Expr{n.X},
					TokPos: n.Pos(),
					Tok:    token.ASSIGN,
					Rhs:    []ast.Expr{expr},
				}
				// We introduced new expressions after
				// typechecking. Let's add them to the type map.
				m.info.Types[one] = m.info.Types[n.X]
				m.info.Types[expr] = m.info.Types[n.X]
				c.Replace(asgn)
			case *ast.UnaryExpr:
				if n.Op == token.ADD {
					c.Replace(n.X)
				}
			}
			return true
		},
		nil)

	return err
}

// typeAst returns the AST for the given type. Used to
// generate variable declarations.
func (m *model) typeAst(t types.Type, p token.Pos) ast.Expr {
	tast, err := parser.ParseExpr(types.TypeString(t,
		// We must qualify the package by name to yield
		// a syntactically correct type ast.
		func(pkg *types.Package) string {
			// If the type is declared in the model package,
			// leave unqualified.
			if pkg.Path() == m.path {
				return ""
			}
			pos := m.fset.Position(p)
			file := m.pkg.Files[pos.Filename]
			// If the package is not imported but the package
			// name is available, we can add the import.
			nameAvail := pkg.Name() != "ad" // ad is reserved
			for _, is := range file.Imports {
				// Remove quotes from the literal value.
				path := is.Path.Value[1 : len(is.Path.Value)-1]
				// Traverse the list of imports to find the
				// name of the file.
				if pkg.Path() == path {
					switch {
					case is.Name == nil:
						return pkg.Name()
					case is.Name.Name == ".":
						return ""
					case is.Name.Name != "_":
						return is.Name.Name
					}
				}
				if is.Name != nil && is.Name.Name == pkg.Name() {
					nameAvail = false
				}
			}
			if nameAvail {
				// The package is not imported, but the package
				// name is available, import it.
				astutil.AddImport(m.fset, file, pkg.Path())
				return pkg.Name()
			}
			// An object has a type which does not have a name
			// in the current file, and the package name is used
			// by another package. We let the programmer fix
			// this by adding a named import.
			panic(fmt.Sprintf("cannot name package %q: "+
				"not imported and name %q is taken",
				pkg.Path(), pkg.Name()))
		}))
	if err != nil {
		panic(fmt.Sprintf("cannot parse type %v: %v",
			t, err))
	}
	return tast
}

// rewrite rewrites the method using tape-writing calls.
func (m *model) rewrite(method *ast.FuncDecl) (err error) {
	// Apply panics on errors. When Apply panics, we return the
	// error as do other functions.
	defer errOnPanic(
		&err,
		m.fset.Position(method.Pos()),
	)()

	// ontape switches rewriting on and off. If pre returns true
	// but ontape is false, Apply traverses the children but
	// they are not rewritten (until ontape is true).
	ontape := false
	astutil.Apply(method,
		// pre focuses on the parts of the tree that are to be
		// rewritten.
		func(c *astutil.Cursor) bool {
			n := c.Node()
			if n != nil && n.Pos() != token.NoPos {
				defer errOnPanic(
					&err,
					m.fset.Position(n.Pos()),
				)()
			}

			switch n := n.(type) {
			case *ast.BasicLit:
				if !isFloat(m.info.TypeOf(n)) {
					return false
				}
			case *ast.CompositeLit:
				pos := m.fset.Position(n.Pos())
				log.Printf("WARNING: %v:%v:%v: composite literals "+
					"are not differentiated yet; see "+
					"https://bitbucket.org/dtolpin/infergo/issues/1.",
					pos.Filename, pos.Line, pos.Column)
				return false
			case *ast.IndexExpr, *ast.SelectorExpr,
				*ast.StarExpr, *ast.UnaryExpr, *ast.BinaryExpr:
				// Expressions must be of type float64
				e := n.(ast.Expr)
				if !isFloat(m.info.TypeOf(e)) {
					return false
				}
			case *ast.Ident:
				if n.Name == "_" {
					// Dummy identifier, a new place will be
					// allocated.
					break
				}
				o := m.info.ObjectOf(n)
				if o == nil {
					return false
				}
				// We only need identifiers which are constants
				// or variables but not fields ...
				if _, ok := c.Parent().(*ast.SelectorExpr); ok {
					return false
				}
				// ... and the type must be float64 or untyped
				// float.
				if !isFloat(m.info.TypeOf(n)) {
					return false
				}
			case *ast.CallExpr:
				switch {
				case m.isDifferentiated(n):
				case m.isElemental(n):
				case m.isVlemental(n):
				default:
					// A function which is neither
					// differentiated nor elemental is called
					// with all their arguments unmodified.
					if isFloat(m.info.TypeOf(n)) {
						value := callExpr("Value", n)
						c.Replace(value)
					}
					return false
				}
			case *ast.ReturnStmt: // if float64
				if len(n.Results) != 1 {
					return false
				}
				for _, r := range n.Results {
					if !isFloat(m.info.TypeOf(r)) {
						return false
					}
				}
				ontape = true
			case *ast.AssignStmt:
				// All expressions are float64.
				for _, r := range n.Rhs {
					t := m.info.TypeOf(r)
					if !isFloat(t) {
						// The returned value is not a float,
						// but it may be a tuple of floats. We
						// cannot differentiate it, but since it
						// appears counterintuitive, we issue
						// a warning here.
						t, ok := t.(*types.Tuple)
						if ok {
							floats := true
							for i := 0; i != t.Len(); i++ {
								if !isFloat(t.At(i).Type()) {
									floats = false
									break
								}
							}
							if floats {
								pos := m.fset.Position(n.Pos())
								log.Printf(
									"WARNING: %v:%v:%v: cannot "+
										"differentiate assignment "+
										"from multiple returned values",
									pos.Filename, pos.Line, pos.Column)
							}
						}
						return false
					}
				}
				// All places are assignable.
				for _, l := range n.Lhs {
					switch l := l.(type) {
					case *ast.IndexExpr:
						_, ok := m.info.TypeOf(l.X).(*types.Map)
						if ok {
							pos := m.fset.Position(l.Pos())
							log.Printf(
								"WARNING: %v:%v:%v: cannot "+
									"differentiate assignment to "+
									"a map entry",
								pos.Filename, pos.Line, pos.Column)
							return false
						}
					}
				}
				ontape = true
			case *ast.ExprStmt: // if a model method call
				call, ok := n.X.(*ast.CallExpr)
				if !ok {
					return false
				}
				if !m.isDifferentiated(call) {
					return false
				}
				ontape = true
			}
			return true
		},

		// post differentiates expressions in bottom-up order.
		func(c *astutil.Cursor) bool {
			if !ontape {
				return true
			}
			n := c.Node()
			if n != nil && n.Pos() != token.NoPos {
				defer errOnPanic(
					&err,
					m.fset.Position(n.Pos()),
				)()
			}

			switch n := n.(type) {
			case *ast.BasicLit:
				value := callExpr("Value", n)
				c.Replace(value)
			case *ast.Ident:
				var place ast.Expr
				if n.Name == "_" {
					// Dummy identifier, allocate a new place.
					place = callExpr("Value", floatExpr(0))
				} else {
					if m.prefix != "" &&
						strings.HasPrefix(n.Name, m.prefix) {
						// Reserved identifier.
						o := m.info.ObjectOf(n)
						if n.Pos() == o.Pos() { // declared here
							panic(fmt.Sprintf(
								"identifier %q is reserved", n.Name))
						}
					}
					o := m.info.ObjectOf(n)
					switch o.(type) {
					case *types.Const:
						place = callExpr("Value", n)
					case *types.Var:
						place = &ast.UnaryExpr{
							Op: token.AND,
							X:  n,
						}
					}
				}
				c.Replace(place)
			case *ast.IndexExpr:
				var place ast.Expr
				if _, ok := m.info.TypeOf(n.X).(*types.Map); ok {
					// Map entries cannot be differentiated
					place = callExpr("Value", n)
				} else {
					place = &ast.UnaryExpr{
						Op: token.AND,
						X:  n,
					}
				}
				c.Replace(place)
			case *ast.SelectorExpr:
				var place ast.Expr
				o := m.info.ObjectOf(n.Sel)
				switch o.(type) {
				case *types.Const:
					place = callExpr("Value", n)
				case *types.Var:
					place = &ast.UnaryExpr{
						Op: token.AND,
						X:  n,
					}
				}
				c.Replace(place)
			case *ast.ReturnStmt:
				ret := callExpr("Return", n.Results...)
				n.Results = []ast.Expr{ret}
				ontape = false
			case *ast.StarExpr:
				c.Replace(n.X)
			case *ast.UnaryExpr:
				switch n.Op {
				case token.SUB:
					var neg ast.Expr
					if neg = m.fold(n); neg == nil {
						neg = callExpr("Arithmetic",
							varExpr("OpNeg"),
							n.X)
					}
					c.Replace(neg)
				default:
					panic(fmt.Sprintf(
						"cannot rewrite unary %v", n.Op))
				}
			case *ast.BinaryExpr:
				var bin ast.Expr
				if bin = m.fold(n); bin == nil {
					bin = callExpr("Arithmetic",
						map[token.Token]ast.Expr{
							token.ADD: varExpr("OpAdd"),
							token.SUB: varExpr("OpSub"),
							token.MUL: varExpr("OpMul"),
							token.QUO: varExpr("OpDiv"),
						}[n.Op],
						n.X, n.Y)
				}
				c.Replace(bin)
			case *ast.AssignStmt:
				var asgn ast.Expr
				if len(n.Lhs) == 1 {
					asgn = callExpr("Assignment",
						n.Lhs[0], n.Rhs[0])
				} else {
					asgn = callExpr("ParallelAssignment",
						append(n.Lhs, n.Rhs...)...)
				}
				stmt := &ast.ExprStmt{X: asgn}
				c.Replace(stmt)
				ontape = false
			case *ast.CallExpr:
				switch {
				case m.isDifferentiated(n):
					// Collect arguments.
					var innerArgs, outerArgs []ast.Expr
					t := m.info.TypeOf(n.Fun).(*types.Signature)
					nparams := t.Params().Len()
					if t.Variadic() {
						nparams--
					}
					nargs := 0
					for i := 0; i != nparams; i++ {
						param := t.Params().At(i)
						if isFloat(param.Type()) {
							// A float, pass 0 to the actual
							// function and the differentiated
							// expression to Call.
							innerArgs = append(innerArgs,
								floatExpr(0))
							outerArgs = append(outerArgs, n.Args[i])
							nargs++
						} else {
							// Anything else, just pass to the
							// actual call.
							innerArgs = append(innerArgs, n.Args[i])
						}
					}

					// If there are no variadic arguments, the
					// wrapper parameter is unused.
					vararg := &ast.Ident{
						Name: "_",
					}
					ellipsis := token.NoPos
					if t.Variadic() && len(n.Args) > nparams {
						variadic := t.Params().At(nparams)
						vt := variadic.Type().(*types.Slice)
						if isFloat(vt.Elem()) &&
							n.Ellipsis == token.NoPos {
							// Variadic float64 arguments, passed
							// through the wrapper parameter.
							vararg = m.genIdent("vararg")
							innerArgs = append(innerArgs, vararg)
							ellipsis = 1
							outerArgs = append(outerArgs,
								n.Args[nparams:]...)
						} else {
							// Either not float or a slice is passed.
							innerArgs = append(innerArgs,
								n.Args[nparams:]...)
							ellipsis = n.Ellipsis
						}
					}

					outerArgs = append([]ast.Expr{intExpr(nargs)},
						outerArgs...)

					differentiated := callExpr("Call",
						append([]ast.Expr{
							callWrapper(vararg,
								n.Fun, innerArgs, ellipsis),
						}, outerArgs...)...)
					c.Replace(differentiated)
				case m.isElemental(n):
					elemental := callExpr("Elemental",
						append([]ast.Expr{n.Fun}, n.Args...)...)
					c.Replace(elemental)
				case m.isVlemental(n):
					vlemental := callExpr("Vlemental",
						append([]ast.Expr{n.Fun}, n.Args...)...)
					c.Replace(vlemental)
				}
			case *ast.ExprStmt:
				ontape = false
			}
			return true
		})

	// Method entry
	// Processed after the traversal so that Apply does not see
	// the added function calls.

	// If we are differentiating Observe, the entry is different
	// than for other methods. Depending on whether Observe was
	// called from another model method (on the same or a
	// different model), or from a unObserve,
	// the prologue is either like of any other method (Enter)
	// or the beginning of a tape frame (Setup). Any other
	// method can only be called from Observe
	// and panicks otherwise.
	var foreign ast.Stmt
	if method.Name.Name == "Observe" {
		foreign = m.setupStmt(method)
	} else {
		foreign = &ast.ExprStmt{
			X: &ast.CallExpr{
				Fun: &ast.Ident{Name: "panic"},
				Args: []ast.Expr{
					&ast.BasicLit{
						Value: fmt.Sprintf(
							"\"%v called outside Observe\"",
							method.Name.Name),
						Kind: token.STRING,
					}}}}
	}
	prologue := &ast.IfStmt{
		Cond: callExpr("Called"),
		Body: &ast.BlockStmt{
			List: []ast.Stmt{
				m.enterStmt(method),
			}},
		Else: &ast.BlockStmt{
			List: []ast.Stmt{foreign}}}
	method.Body.List = append([]ast.Stmt{prologue},
		method.Body.List...)

	return err
}

// setupStmt  returns the ast for the Setup or Enter
// conditional at the beginning of an Observe method.
func (m *model) setupStmt(method *ast.FuncDecl) ast.Stmt {
	param := method.Type.Params.List[0]
	var arg ast.Expr
	if len(param.Names) == 0 || param.Names[0].Name == "_" {
		// The parameter is _; the argument is an empty
		// slice.
		arg = &ast.CompositeLit{
			Type: param.Type,
		}
	} else {
		arg = param.Names[0]
	}
	setup := &ast.ExprStmt{X: callExpr("Setup", arg)}
	return setup
}

// enterStmt returns the ast for the Enter statement
// at the beginning of a model method.
func (m *model) enterStmt(method *ast.FuncDecl) ast.Stmt {
	// Collect float64 parameters. Their values are copied
	// from the tape.
	t := m.info.TypeOf(method.Name).(*types.Signature)
	var params []ast.Expr
	n := t.Params().Len()
	if t.Variadic() {
		// If the function is variadic, the last parameter
		// is not a float64.
		n--
	}
	// Signature parameters are flat, but ast parameters are
	// two-dimensional: a parameter is a Field with possibly
	// multiple names in it.
	iparam, ifield := 0, 0 // ast indices
	for i := 0; i != n; i++ {
		p := t.Params().At(i)
		if isFloat(p.Type()) {
			var expr ast.Expr
			if p.Name() == "_" {
				// There is no variable to copy the value to,
				// create a dummy value.
				expr = callExpr("Value", floatExpr(0))
			} else {
				expr = &ast.UnaryExpr{
					Op: token.AND,
					X: method.Type.Params.List[iparam].
						Names[ifield],
				}
			}
			params = append(params, expr)
		}
		ifield++
		if ifield == len(method.Type.Params.List[iparam].Names) {
			iparam++
			ifield = 0
		}
	}
	enter := &ast.ExprStmt{X: callExpr("Enter", params...)}
	return enter
}

// fold constant-folds the expression, if possible. If the expression
// is not constant, nil is returned.
func (m *model) fold(expr ast.Expr) ast.Expr {
	if !Fold {
		return nil
	}
	if v := m.info.Types[expr].Value; v != nil {
		if fv, known := constant.Float64Val(v); known {
			return callExpr("Value", floatExpr(fv))
		}
	}
	return nil
}

// genIdent returns generated identifier based on the name.
func (m *model) genIdent(name string) *ast.Ident {
	return &ast.Ident{
		Name: m.prefix + name,
	}
}

// isDifferentiated returns true iff the call is of a
// differentiated method
func (m *model) isDifferentiated(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ok
	}
	t, ok := m.info.Selections[sel]
	if !ok {
		return ok
	}
	ok = t.Kind() == types.MethodVal && m.isMethodType(t.Type())

	if ok {
		// Fix the import: if the import refers to the
		// undifferentiated package, add the "/ad" suffix
		// to the first import of the package. Remaining
		// imports of the same package, with different
		// names, can be used to access the undifferentiated
		// version of the package.

		// We are using TypeString with a custom qualifier here
		// to get access to the receiver type's package. This is
		// slightly perversive, but does the job.
		types.TypeString(t.Recv(),
			func(pkg *types.Package) string {
				if strings.HasSuffix(pkg.Path(), "/ad") {
					// We are already using a differentiated
					// model, do nothing.
					return pkg.Path()
				}
				adPath := pkg.Path() + "/ad"
				pos := m.fset.Position(call.Pos())
				file := m.pkg.Files[pos.Filename]
				// Traverse the list of imports to find the
				// name of the file.
				for _, is := range file.Imports {
					// Remove quotes from the literal value.
					path := is.Path.Value[1 : len(is.Path.Value)-1]
					if adPath == path {
						// Prefix already added, stop.
						break
					}
					if pkg.Path() == path {
						// Found, add the /ad suffix.
						is.Path.Value = `"` + adPath + `"`
						break
					}
				}
				return pkg.Path()
			})
	}

	return ok
}

// isElemental returns true iff the call is of an elemental
// function. An elemental function is a function with one or
// more non-variadic float64 parameters returning float64.
// isElemental does not check whether this is a differentiated
// function instead and should be called after isDifferentiated.
func (m *model) isElemental(call *ast.CallExpr) bool {
	t, ok := m.info.TypeOf(call.Fun).(*types.Signature)
	if !ok { // a type cast rather than a call
		return false
	}
	if t.Results().Len() != 1 {
		return false
	}
	if !isFloat(t.Results().At(0).Type()) {
		return false
	}

	if t.Params().Len() == 0 || t.Variadic() {
		return false
	}
	for i := 0; i != t.Params().Len(); i++ {
		if !isFloat(t.Params().At(i).Type()) {
			return false
		}
	}

	return true
}

// isVlemental returns true iff the call is of a vector
// elemental function. An elemental function is a function with
// one argument of []float64 type returning float64.
// isVlemental does not check whether this is a differentiated
// function instead and should be called after isDifferentiated.
func (m *model) isVlemental(call *ast.CallExpr) bool {
	t, ok := m.info.TypeOf(call.Fun).(*types.Signature)
	if !ok { // a type cast rather than a call
		return false
	}
	if t.Results().Len() != 1 || t.Variadic() {
		return false
	}
	if !isFloat(t.Results().At(0).Type()) {
		return false
	}

	if t.Params().Len() != 1 {
		return false
	}
	st, ok := t.Params().At(0).Type().(*types.Slice)
	if !ok {
		return false
	}
	if !isFloat(st.Elem()) {
		return false
	}

	return true
}

// isFloat returns true iff the kind is a float kind
func isFloat(typ types.Type) bool {
	bt, ok := typ.(*types.Basic)
	return ok &&
		(bt.Kind() == types.Float64 ||
			bt.Kind() == types.UntypedFloat)
}

// intExpr returns an Expr for integer literal i.
func intExpr(i int) ast.Expr {
	return &ast.BasicLit{
		Kind:  token.INT,
		Value: fmt.Sprintf("%v", i),
	}
}

// floatExpr returns an Expr for floating point literal x.
func floatExpr(x float64) ast.Expr {
	return &ast.BasicLit{
		Kind:  token.FLOAT,
		Value: fmt.Sprintf("%v", x),
	}
}

// varExpr returns an Expr for variable or constant 'ad.name'.
func varExpr(name string) ast.Expr {
	return &ast.SelectorExpr{
		X: &ast.Ident{
			Name: "ad",
		},
		Sel: &ast.Ident{
			Name: name,
		},
	}
}

// callExpr returns an Expr for call 'ad.name(args...)'.
func callExpr(name string, args ...ast.Expr) ast.Expr {
	return &ast.CallExpr{
		Fun:  varExpr(name),
		Args: args,
	}
}

// callWrapper retUrns an Expr for a wrapped differentiated
// call.
func callWrapper(
	vararg *ast.Ident,
	fun ast.Expr,
	args []ast.Expr,
	ellipsis token.Pos,
) *ast.FuncLit {
	return &ast.FuncLit{
		Type: &ast.FuncType{
			Params: &ast.FieldList{
				List: []*ast.Field{
					&ast.Field{
						Names: []*ast.Ident{
							vararg,
						},
						Type: &ast.ArrayType{
							Elt: &ast.Ident{
								Name: "float64",
							}}}}}},
		Body: &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.ExprStmt{
					X: &ast.CallExpr{
						Fun:      fun,
						Args:     args,
						Ellipsis: ellipsis,
					}}}},
	}
}

// Writing

// write writes the differentiated model as a Go package source.
func (m *model) write() (err error) {
	admpath := filepath.Join(m.path, "ad")
	// Create the directory for the differentiated model.
	err = os.Mkdir(admpath, os.ModePerm)
	if err != nil &&
		// The only error we can tolerate is that the directory
		// already exists (for example from an earlier
		// differentiation).
		!os.IsExist(err) {
		return err
	}
	err = nil

	// Write files to the ad subpackage under the same names.
	for fpath, file := range m.pkg.Files {
		_, fname := filepath.Split(fpath)
		f, err := os.Create(filepath.Join(admpath, fname))
		defer f.Close()
		if err != nil {
			return err
		}
		w := bufio.NewWriter(f)
		defer w.Flush()
		printer.Fprint(w, m.fset, file)
	}

	return err
}
