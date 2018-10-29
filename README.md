# Learning programs in Go

Go as a platform for probabilistic inference. Uses
automatic differentiation and gradient descent
optimization.

MIT license (see [LICENSE](LICENSE))

## Example

```Go
// Learning the parameters of the Normal distribution from
// observations
package normal

import "math"

// data are the observations
type Model struct {
	Data []float64
}

// x[0] is the mean, x[1] is the logvariance of the distribution
func (m *Model) Observe(x []float64) float64 {
    mean := x[0]
    logv := x[1]
    vari := math.Exp(logv)
    ll := 0.
    for i := 0; i != len(m.Data); i++ {
        d := m.Data[i] - mean
        ll -= d*d/vari + logv
    }
	return ll
}
```
