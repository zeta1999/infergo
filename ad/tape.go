package ad

// Implementation of the AD tape

import (
	"fmt"
	"reflect"
)

// There is one global tape.
var tape oneGlobalTape

// A tape is a list of records and the memory
type oneGlobalTape struct {
	records    []record             // recorded instructions
	places     []*float64           // variable places
	values     []float64            // stored values
	elementals []elemental          // gradients of elementals
	cstack     []counters           // counter stack (see below)
	adj         []int                // adjoint indices
	adv         []float64            // adjoint values
}

func init() {
	tape.records = append(tape.records, record{typ: typDummy})
}

// A record specifies the record type and indexes the tape
// memory to specify the record arguments. At the cost of one
// redirection, the number of memory allocations is logarithmic
// in the number of instructions, and a record has a fixed size.
type record struct {
	typ, op int //  record type and opcode*
	p, v    int // indices of the first place and value
	// * for elementals, op is the index of gradient
	//   for assignments, op is the number of values
}

// The structure elemental stores information required
// to compute the gradient.
type elemental struct {
	n int      // number of arguments
	g gradient // gradient
}

// The counters structure holds counters for the tape
// components. Counters are pushed onto stack for repeated
// calls to automatic differentiation (e.e. for nested
// inference).
type counters struct {
	n, // independents
	r, // records
	p, // places
	v, // values
	e int // elementals
}

// Record types
const (
	typDummy      = iota // placeholder
	typAssignment        // assignment statement
	typArithmetic        // unary or binary
	typElemental         // call to an elemental function
	typCall              // last on the tape before a method call
)

// arithmetic operations
const (
	OpNeg = iota
	OpAdd
	OpSub
	OpMul
	OpDiv
)

// Forward pass

// Setup set ups the tape for the forward pass.
func Setup(x []float64) {
	push(len(x))
	// The returned value is in the first place;
	// see Call and Return below.
	tape.places = append(tape.places, Value(0.))
	register(x)
}

// push pushes a counter frame to the counter stack.
// n is the number of function parameters.
func push(n int) {
	c := counters{
		n: n,
		r: len(tape.records),
		p: len(tape.places),
		v: len(tape.values),
		e: len(tape.elementals),
	}
	tape.cstack = append(tape.cstack, c)
}

// register stores locations of function parameters
// at the beginning of the current frame's places.
// The places are then used to collect the partial
// derivatives of the gradient.
func register(x []float64) {
	for i := 0; i != len(x); i++ {
		tape.places = append(tape.places, &x[i])
	}
}

// Constant adds value v to the memory and returns
// the location of the value.
func Value(v float64) *float64 {
	tape.values = append(tape.values, v)
	return &tape.values[len(tape.values)-1]
}

// Return returns the result of the differentiated function.
func Return(px *float64) float64 {
	// The returned value goes into the first place.
	c := &tape.cstack[len(tape.cstack) -1]
	tape.places[c.p] = px
	return *px
}

// Arithmetic encodes an arithmetic operation and returns
// the location of the result.
func Arithmetic(op int, px ...*float64) *float64 {
	p := Value(0.)

	// Register
	r := record{
		typ: typArithmetic,
		op:  op,
		p:   len(tape.places),
	}
	tape.places = append(tape.places, p)
	tape.places = append(tape.places, px...)
	tape.records = append(tape.records, r)

	// Run
	switch op {
	case OpNeg:
		*p = -*px[0]
	case OpAdd:
		*p = *px[0] + *px[1]
	case OpSub:
		*p = *px[0] - *px[1]
	case OpMul:
		*p = *px[0] * *px[1]
	case OpDiv:
		*p = *px[0] / *px[1]
	default:
		panic(fmt.Sprintf("bad opcode %v", r.op))
	}

	return p
}

// ParallelAssigment encodes a parallel assignment.
func ParallelAssignment(ppx ...*float64) {
	// Register
	p, px := ppx[:len(ppx)/2], ppx[len(ppx)/2:]
	r := record{
		typ: typAssignment,
		op:  len(p),
		p:   len(tape.places),
		v:   len(tape.values),
	}
	for i := range p {
		tape.places = append(tape.places, p[i])
		tape.values = append(tape.values, *p[i])
	}
	for i := range px {
		tape.places = append(tape.places, px[i])
		tape.values = append(tape.values, *px[i])
	}
	tape.records = append(tape.records, r)

	// Run
	for i := range p {
		*p[i] = tape.values[len(tape.values)-len(p)+i]
	}
}

// Assignment encodes a single-value assingment.
func Assignment(p *float64, px *float64) {
	// Can be just a call to ParallelAssignment.
	// However most assignments are single-valued and
	// we can avoid loops and extra allocation.

	// Register
	r := record{
		typ: typAssignment,
		op:  1,
		p:   len(tape.places),
		v:   len(tape.values),
	}
	tape.places = append(tape.places, p, px)
	tape.values = append(tape.values, *p)
	tape.records = append(tape.records, r)

	// Run
	*p = *px
}

// Elemental encodes a call to the elemental f.
// To call gradient without allocation on backward pass,
// argument values are copied to the tape memory.
// Elemental returns the location of the result.
func Elemental(f interface{}, px ...*float64) *float64 {
	p := Value(0.)

	g, ok := ElementalGradient(f)
	if !ok {
		// actually an elemental
		panic("not an elemental")
	}

	// Register
	r := record{
		typ: typElemental,
		op:  len(tape.elementals),
		p:   len(tape.places),
		v:   len(tape.values),
	}
	e := elemental{
		n: len(px),
		g: g,
	}
	tape.places = append(tape.places, p)
	tape.places = append(tape.places, px...)
	for _, py := range px {
		tape.values = append(tape.values, *py)
	}
	tape.elementals = append(tape.elementals, e)
	tape.records = append(tape.records, r)

	// Run
	// Any elemental function can be called, but one- and
	// two-argument elementals are called efficiently, without
	// allocation; other types are called through reflection.
	switch f := f.(type) {
	case func(float64) float64:
		*p = f(*px[0])
	case func(float64, float64) float64:
		*p = f(*px[0], *px[1])
	default:
		args := make([]reflect.Value, 0)
		for _, py := range px {
			args = append(args, reflect.ValueOf(*py))
		}
		*p = reflect.ValueOf(f).Call(args)[0].Float()
	}

	return p
}

// Calling differentiated functions

// True iff the last record on the tape is a Call record.
// A call record is added before a call to a differentiated
// method from another differentiated method.
func Called() bool {
	return tape.records[len(tape.records)-1].typ == typCall
}

// Call wraps a call to a differentiated subfunction. narg is
// the number of non-variadic arguments.
func Call(
	f func(_vararg []float64),
	narg int,
	px ...*float64,
) *float64 {
	// Register function parameters. The function will assign
	// the actual parameters to the formal parameters on entry.
	var vararg []float64
	if narg < len(px) {
		vararg = variadic(px[narg:])
	}
	for _, py := range px[:narg] {
		tape.places = append(tape.places, py)
	}
	// Let the method know that it was called from
	// another method.
	icall := len(tape.records)
	tape.records = append(tape.records, record{typ: typCall})
	f(vararg)
	// Call records are biodegradable; should be never
	// seen during the backward pass.
	tape.records[icall].typ = typDummy
	//
	// If the function returns a float64 value, the returned
	// value is in the first place. Otherwise, the function
	// is called as an expression statement, for side effects,
	// and the return value is ignored.
	c := &tape.cstack[len(tape.cstack) -1]
	return tape.places[c.p]
}

// variadic wraps variadic arguments into a slice for passing to
// the underlying call.
func variadic(px []*float64) []float64 {
	// In order to pass variadic float64 arguments to a
	// differentiated method, we build a slice on the caller
	// side and assign the arguments to the slice. We put the
	// slice onto the tape.
	var sides []*float64
	v0 := len(tape.values)
	for range px { // left-hand side
		sides = append(sides, Value(0.))
	}
	vararg := tape.values[v0:]   // the slice
	sides = append(sides, px...) // right-hand side
	ParallelAssignment(sides...)
	// Now, the result of variadic is a slice, to be passed
	// to the variadic argument.
	return vararg
}

// Enter copies the actual parameters to the formal parameters.
func Enter(px ...*float64) {
	p0 := len(tape.places) - len(px)
	ParallelAssignment(append(px, tape.places[p0:p0+len(px)]...)...)
}

// Backward pass

// Gradient performs the backward pass on the tape and returns
// the gradient. It should be called immediately after the call
// to an automatically differentiated function, and can be
// called only once per call to an automatically differentiated
// function.
func Gradient() []float64 {
	backward()
	partials := partials()
	Pop()
	return partials
}

// Pop deallocates current tape fragment from the tape.
// Gradient calls Pop; when the gradient is not needed, Pop can
// be called directly to skip gradient computation.
func Pop() {
	c := &tape.cstack[len(tape.cstack)-1]
	tape.records = tape.records[:c.r]
	tape.places = tape.places[:c.p]
	tape.values = tape.values[:c.v]
	tape.elementals = tape.elementals[:c.e]
	tape.cstack = tape.cstack[:len(tape.cstack)-1]
}

// backward runs the backward pass on the tape and returns the
// adjoints.
func backward() {
	c := &tape.cstack[len(tape.cstack)-1]

	// We can use map[*float64]float64, however map access is slow.
	// Therefore we build a two-level dereference structure where
	// we access each entry once.
	if len(tape.adj) < len(tape.places) {
		tape.adj = append(tape.adj,
			make([]int, len(tape.places)-len(tape.adj))...)
	}
	adjoints := make(map[*float64]int, len(tape.places)-c.p)
	for i := c.p; i != len(tape.places); i++ {
		if j, exists := adjoints[tape.places[i]]; exists {
			tape.adj[i] = j
		} else {
			j = len(tape.adv)
			tape.adj[i] = j
			adjoints[tape.places[i]] = j
			tape.adv = append(tape.adv, 0.)
		}
	}

	values, places, elementals, adj, adv := 
		tape.values, tape.places, tape.elementals, tape.adj, tape.adv

	// Set the adjoint of the result to 1.
	adv[adj[0]] = 1.
	// Bottom is the first record in the current frame.
	bottom := c.r
	for ir := len(tape.records); ir != bottom; {
		ir--
		r := &tape.records[ir]
		// Only assignment may have the same location
		// on both the right-hand and the left-hand.
		switch r.typ {
		case typDummy: // do nothing, obviously
		case typAssignment: // x; d/dx = 1
			if r.op == 1 { // Most assignments are single-valued
				// Restore the previous value.
				*places[r.p] = values[r.v]
				// Save the adjoint.
				a := adv[adj[r.p]]
				// Update the adjoint: the adjoint of the left-hand
				// side is zero (because it is overwritten) except
				// if the right-hand side is the same location.
				adv[adj[r.p]] = 0.
				adv[adj[r.p+1]] += a
			} else {
				// Restore the previous values.
				for i := 0; i != r.op; i++ {
					*places[r.p+i] = values[r.v+i]
				}
				// Save the adjoints.
				// a is a vector, re-use values.
				a := values[r.v : r.v+r.op]
				for i := 0; i != r.op; i++ {
					a[i] = adv[adj[r.p+i]]
				}
				// Update the adjoints: the adjoint of the left-hand
				// side is zero (because it is overwritten) except
				// if the right-hand side is the same location.
				for i := 0; i != r.op; i++ {
					adv[adj[r.p+i]] = 0.
				}
				for i := 0; i != r.op; i++ {
					adv[adj[r.p+r.op+i]] += a[i]
				}
			}
		case typArithmetic:
			a := adv[adj[r.p]]
			switch r.op {
			case OpNeg: // -x; d/dx = -1
				adv[adj[r.p+1]] -= a
			case OpAdd: // x + y; d/dx = 1; d/dy = 1
				adv[adj[r.p+1]] += a
				adv[adj[r.p+2]] += a
			case OpSub: // x - y; d/dx = 1; d/dy = -1
				adv[adj[r.p+1]] += a
				adv[adj[r.p+2]] -= a
			case OpMul: // x * y; d/dx = y; d/dy = x
				ax := a * *places[r.p+2]
				ay := a * *places[r.p+1]
				adv[adj[r.p+1]] += ax
				adv[adj[r.p+2]] += ay
			case OpDiv: // x / y; d/dx = 1 / y; d/dy = - d/dx * p
				ax := a / *places[r.p+2]
				ay := -ax * *places[r.p]
				adv[adj[r.p+1]] += ax
				adv[adj[r.p+2]] += ay
			default:
				panic(fmt.Sprintf("bad opcode %v", r.op))
			}
		case typElemental: // f(x, y, ...)
			a := adv[adj[r.p]]
			e := &elementals[r.op]
			d := e.g(*places[r.p],
				// Parameters must be copied to tape.values during
				// the forward pass.
				values[r.v:r.v+e.n]...)
			for i := 0; i != e.n; i++ {
				adv[adj[r.p+1+i]] += a * d[i]
			}
		default:
			panic(fmt.Sprintf("bad type %v", r.typ))
		}
	}
}

// partials collects the partial derivatives from the adjoints;
// places 1 to c.n are parameters.
func partials() []float64 {
	c := &tape.cstack[len(tape.cstack)-1]
	partials := make([]float64, c.n)
	for i := 0; i != c.n; i++ {
		partials[i] = tape.adv[tape.adj[c.p+i+1]]
	}
	return partials
}
