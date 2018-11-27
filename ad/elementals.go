package ad

import (
	"math"
	"reflect"
)

// ElementalGradientFunc accepts the function value and the
// parameters and returns a vector of partial gradients.
// Depending on the function, either the value or the parameters
// may be ignored in the computation of the gradient.
type ElementalGradientFunc func(value float64, params ...float64) []float64

var elementals map[uintptr]ElementalGradientFunc

// fkey computes map key for a function.
func fkey(f interface{}) uintptr {
	return reflect.ValueOf(f).Pointer()
}

// RegisterElemental registers the gradient for an elemental
// function.
func RegisterElemental(f interface{}, g ElementalGradientFunc) {
	elementals[fkey(f)] = g
}

// ElementalGradient returns the gradient for a function.  If
// the function is not registered as elemental, the second
// returned value is false.
func ElementalGradient(f interface{}) (ElementalGradientFunc, bool) {
	g, ok := elementals[fkey(f)]
	return g, ok
}

// Elementals from the math package.
func init() {
	elementals = make(map[uintptr]ElementalGradientFunc)
	RegisterElemental(math.Sqrt,
		func(value float64, _ ...float64) []float64 {
			return []float64{0.5 / value}
		})

	// Exponential and logarithmic
	RegisterElemental(math.Exp,
		func(value float64, _ ...float64) []float64 {
			return []float64{value}
		})
	RegisterElemental(math.Log,
		func(_ float64, params ...float64) []float64 {
			return []float64{1. / params[0]}
		})
	RegisterElemental(math.Pow,
		func(value float64, params ...float64) []float64 {
			return []float64{value * math.Log(params[0])}
		})

	// Trigonometric
	RegisterElemental(math.Sin,
		func(_ float64, params ...float64) []float64 {
			return []float64{math.Cos(params[0])}
		})
	RegisterElemental(math.Cos,
		func(_ float64, params ...float64) []float64 {
			return []float64{-math.Sin(params[0])}
		})
	RegisterElemental(math.Tan,
		func(value float64, _ ...float64) []float64 {
			return []float64{1. + value*value}
		})
}
