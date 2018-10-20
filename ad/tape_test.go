package ad

// Testing the tape

import (
	"math"
	"reflect"
	"testing"
)

// ddx differentiates the function passed in
// and returns the gradient.
func ddx(x []float64, f func(x []float64)) []float64 {
	Setup(x)
	f(x)
	return Gradient()
}

// Tape management

// When we pop we must return to where we were before
func TestPop(t *testing.T) {
	// Top-level differentiation
	shouldPop(t, []float64{0., 1.}, func(x []float64) {
		Assignment(&x[1], Arithmetic(OpAdd, &x[0], &x[1]))
	})
	ddx([]float64{1.}, func(x []float64) {
		Assignment(&x[0], Place(Value(1.)))
	})
	// Nested differentiation
	shouldPop(t, []float64{0., 1.}, func(x []float64) {
		Assignment(&x[1], Arithmetic(OpAdd, &x[0], &x[1]))
	})
}

func shouldPop(test *testing.T, x []float64, f func(x []float64)) {
	lr := len(t.records)
	lp := len(t.places)
	lv := len(t.values)
	le := len(t.elementals)
	lc := len(t.cstack)
	ddx([]float64{0., 1.}, f)
	if lr != len(t.records) {
		test.Errorf("wrong number of records: got %d, want %d",
			len(t.records), lr)
	}
	if lp != len(t.places) {
		test.Errorf("wrong number of places: got %d, want %d",
			len(t.places), lp)
	}
	if lv != len(t.values) {
		test.Errorf("wrong number of values: got %d, want %d",
			len(t.values), lv)
	}
	if le != len(t.elementals) {
		test.Errorf("wrong number of elementals: got %d, want %d",
			len(t.elementals), le)
	}
	if lc != len(t.cstack) {
		test.Errorf("wrong number of counters: got %d, want %d",
			len(t.cstack), lc)
	}
}

// Differentiation rules

// testcase defines a test of a single expression on
// several inputs.
type testcase struct {
	s string
	f func(x []float64)
	v [][][]float64
}

// runsuite evaluates a sequence of test cases.
func runsuite(t *testing.T, suite []testcase) {
	for _, c := range suite {
		for _, v := range c.v {
			g := ddx(v[0], c.f)
			if !reflect.DeepEqual(g, v[1]) {
				t.Errorf("%s, x=%v: g=%v, wanted g=%v",
					c.s, v[0], g, v[1])
			}
		}
	}
}

func TestPrimitive(t *testing.T) {
	runsuite(t, []testcase{
		{"x = y",
			func(x []float64) {
				Assignment(Place(Value(0.)), &x[0])
			},
			[][][]float64{
				{{0.}, {1.}},
				{{1.}, {1.}}}},
		{"x = x",
			func(x []float64) {
				Assignment(&x[0], &x[0])
			},
			[][][]float64{
				{{0.}, {1.}},
				{{1.}, {1.}}}},
		{"x + y",
			func(x []float64) {
				Arithmetic(OpAdd, &x[0], &x[1])
			},
			[][][]float64{
				{{0., 0.}, {1., 1.}},
				{{3., 5.}, {1., 1.}}}},
		{"x + x",
			func(x []float64) {
				Arithmetic(OpAdd, &x[0], &x[0])
			},
			[][][]float64{
				{{0.}, {2.}},
				{{1.}, {2.}}}},
		{"x - z",
			func(x []float64) {
				Arithmetic(OpSub, &x[0], &x[1])
			},
			[][][]float64{
				{{0., 0.}, {1., -1.}},
				{{1., 1.}, {1., -1.}}}},
		{"x - x",
			func(x []float64) {
				Arithmetic(OpSub, &x[0], &x[0])
			},
			[][][]float64{
				{{0.}, {0.}},
				{{1.}, {0.}}}},
		{"x * y",
			func(x []float64) {
				Arithmetic(OpMul, &x[0], &x[1])
			},
			[][][]float64{
				{{0., 0.}, {0., 0.}},
				{{2., 3.}, {3., 2.}}}},
		{"x * x",
			func(x []float64) {
				Arithmetic(OpMul, &x[0], &x[0])
			},
			[][][]float64{
				{{0.}, {0.}},
				{{1.}, {2.}}}},
		{"x / y",
			func(x []float64) {
				Arithmetic(OpDiv, &x[0], &x[1])
			},
			[][][]float64{
				{{0., 1.}, {1., 0.}},
				{{2., 4.}, {0.25, -0.125}}}},
		{"x / x",
			func(x []float64) {
				Arithmetic(OpDiv, &x[0], &x[0])
			},
			[][][]float64{
				{{1.}, {0.}},
				{{2.}, {0.}}}},
		{"sqrt(x)",
			func(x []float64) {
				Elemental(math.Sqrt, &x[0])
			},
			[][][]float64{
				{{0.25}, {1.}},
				{{1.}, {0.5}},
				{{4.}, {0.25}}}},
		{"log(x)",
			func(x []float64) {
				Elemental(math.Log, &x[0])
			},
			[][][]float64{
				{{1.}, {1.}},
				{{2.}, {0.5}}}},
		{"exp(x)",
			func(x []float64) {
				Elemental(math.Exp, &x[0])
			},
			[][][]float64{
				{{0.}, {1.}},
				{{1.}, {math.E}}}},
		{"cos(x)",
			func(x []float64) {
				Elemental(math.Cos, &x[0])
			},
			[][][]float64{
				{{0.}, {0.}},
				{{1.}, {-math.Sin(1.)}}}},
		{"sin(x)",
			func(x []float64) {
				Elemental(math.Sin, &x[0])
			},
			[][][]float64{
				{{0.}, {1.}},
				{{1.}, {math.Cos(1.)}}}},
	})
}

func TestComposite(t *testing.T) {
	runsuite(t, []testcase{
		{"x * x + y * y",
			func(x []float64) {
				Arithmetic(OpAdd,
					Arithmetic(OpMul, &x[0], &x[0]),
					Arithmetic(OpMul, &x[1], &x[1]))
			},
			[][][]float64{
				{{0., 0.}, {0., 0.}},
				{{1., 1.}, {2., 2.}},
				{{2., 3.}, {4., 6.}}}},
		{"(x + y) * (x + y)",
			func(x []float64) {
				Arithmetic(OpMul,
					Arithmetic(OpAdd, &x[0], &x[1]),
					Arithmetic(OpAdd, &x[0], &x[1]))
			},
			[][][]float64{
				{{0., 0.}, {0., 0.}},
				{{1., 1.}, {4., 4.}},
				{{2., 3.}, {10., 10.}}}},
		{"sin(x * y)",
			func(x []float64) {
				Elemental(math.Sin,
					Arithmetic(OpMul, &x[0], &x[1]))
			},
			[][][]float64{
				{{0., 0.}, {0., 0.}},
				{{1., math.Pi}, {-math.Pi, -1.}},
				{{math.Pi, 1.}, {-1., -math.Pi}}}},
	})
}

func TestAssignment(t *testing.T) {
	runsuite(t, []testcase{
		{"z = sin(x * y); v1 = z",
			func(x []float64) {
				Assignment(Place(Value(0.)),
					Elemental(math.Sin,
						Arithmetic(OpMul, &x[0], &x[1])))
			},
			[][][]float64{
				{{0., 0.}, {0., 0.}},
				{{1., math.Pi}, {-math.Pi, -1.}},
				{{math.Pi, 1.}, {-1., -math.Pi}}}},
		{"x = 2.; z = x * x",
			func(x []float64) {
				Assignment(&x[0], Place(Value(2.)))
				Arithmetic(OpMul, &x[0], &x[0])
			},
			[][][]float64{
				{{0.}, {0.}},
				{{3.}, {0.}}}},
		{"x = x; z = x * x",
			func(x []float64) {
				Assignment(&x[0], &x[0])
				Arithmetic(OpMul, &x[0], &x[0])
			},
			[][][]float64{
				{{0.}, {0.}},
				{{3.}, {6.}}}},
	})
}

// elementals to check calling with different signatures
func twoArgElemental(a, b float64) float64 {
	return a * b
}

func threeArgElemental(a, b, c float64) float64 {
	return a + b + c
}

func variadicElemental(a ...float64) float64 {
	return a[0] - a[1]
}

func init() {
	RegisterElemental(twoArgElemental,
		func(v float64, a ...float64) []float64 {
			return []float64{a[1], a[0]}
		})
	RegisterElemental(threeArgElemental,
		func(v float64, a ...float64) []float64 {
			return []float64{1., 1., 1.}
		})
	RegisterElemental(variadicElemental,
		func(v float64, a ...float64) []float64 {
			return []float64{1., -1.}
		})
}

func TestElemental(t *testing.T) {
	runsuite(t, []testcase{
		{"twoArgElemental(x, y)",
			func(x []float64) {
				Elemental(twoArgElemental, &x[0], &x[1])
			},
			[][][]float64{
				{{0., 0.}, {0., 0.}},
				{{1., 2.}, {2., 1.}}}},
		{"threeArgElemental(x, y, t)",
			func(x []float64) {
				Elemental(threeArgElemental, &x[0], &x[1], &x[2])
			},
			[][][]float64{
				{{0., 0., 0.}, {1., 1., 1.}},
				{{1., 2., 3.}, {1., 1., 1.}}}},
		{"variadicElemental(x, y)",
			func(x []float64) {
				Elemental(variadicElemental, &x[0], &x[1])
			},
			[][][]float64{
				{{0., 0.}, {1., -1.}},
				{{1., 2.}, {1., -1.}}}},
	})
}

func TestCall(t *testing.T) {
	runsuite(t, []testcase{
		{"(x -> x)(x)",
			func(x []float64) {
				Call(func() {
					func(a float64) float64 {
						Enter(&a)
						return Return(&a)
					}(x[0])
				}, &x[0])
			},
			[][][]float64{
				{{0.}, {1.}},
				{{1.}, {1.}}}},
		{"(x -> x * x)(x)",
			func(x []float64) {
				Call(func() {
					func(a float64) float64 {
						Enter(&a)
						return Return(Arithmetic(OpMul, &a, &a))
					}(x[0])
				}, &x[0])
			},
			[][][]float64{
				{{0.}, {0.}},
				{{1.}, {2.}},
				{{2.}, {4.}}}},
		{"y = (x -> x * x)(x)",
			func(x []float64) {
				Assignment(Place(Value(0.)),
					Call(func() {
						func(a float64) float64 {
							Enter(&a)
							return Return(Arithmetic(OpMul, &a, &a))
						}(x[0])
					}, &x[0]))
			},
			[][][]float64{
				{{0.}, {0.}},
				{{1.}, {2.}},
				{{2.}, {4.}}}},
		{"(x, y -> x + y)(x, y)",
			func(x []float64) {
				Call(func() {
					func(a, b float64) float64 {
						Enter(&a, &b)
						return Return(Arithmetic(OpAdd, &a, &b))
					}(x[0], x[1])
				}, &x[0], &x[1])
			},
			[][][]float64{
				{{0., 0.}, {1., 1.}},
				{{1., 2.}, {1., 1.}}}},
		{"(px, py -> *px = *py)(&x, &y); x + y",
			func(x []float64) {
				Call(func() {
					func(a, b *float64) {
						Assignment(a, b)
					}(&x[0], &x[1])
				})
				Arithmetic(OpAdd, &x[0], &x[1])
			},
			[][][]float64{
				{{0., 0.}, {0., 2.}},
				{{1., 2.}, {0., 2.}}}},
		{"(px, py -> *px = *py)(&x, &y); x * y",
			func(x []float64) {
				Call(func() {
					func(a, b *float64) {
						Assignment(a, b)
					}(&x[0], &x[1])
				})
				Arithmetic(OpMul, &x[0], &x[1])
			},
			[][][]float64{
				{{0., 0.}, {0., 0.}},
				{{1., 2.}, {0., 4.}},
				{{1., 3.}, {0., 6.}}}},
	})
}