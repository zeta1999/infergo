package infer

// Stochastic gradient Markov Chain Monte Carlo variants

import (
	"bitbucket.org/dtolpin/infergo/ad"
	"bitbucket.org/dtolpin/infergo/model"
	"log"
	"math"
	"math/rand"
)


// Stochastic gradient Hamiltonian Monte Carlo
type SgHMC struct {
	sampler
	// Parameters
	L     int     // number of steps
	Eta   float64 // learning rate
	Alpha float64 // friction (1 - momentum)
	V     float64 // diffusion
}

func (sghmc *SgHMC) Sample(
	m model.Model,
	x []float64,
	samples chan []float64,
) {
	sghmc.setDefaults()
	sghmc.samples = samples // Stop needs access to samples
	go func() {
		// On exit:
		// * drop the tape;
		defer ad.DropTape()
		// * close samples;
		defer close(samples)
		// * intercept errors deep inside the algorithm
		// and report them.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("ERROR: SgHMC: %v", r)
			}
		}()

		beta := math.Min(0.5*sghmc.Eta*sghmc.V, sghmc.Alpha)
		sigma := math.Sqrt(2 * sghmc.Eta * (sghmc.Alpha - beta))

		r := make([]float64, len(x))
		for {
			if sghmc.stop {
				break
			}
			// For compatibility with HMC, we advance L steps
			// before each sample
			for istep := 0; istep != sghmc.L; istep++ {
				_, grad := m.Observe(x), model.Gradient(m)
				for j := range r {
					r[j] += sghmc.Eta*grad[j] - sghmc.Alpha*r[j] +
						rand.NormFloat64()*sigma
					x[j] += r[j]
				}
			}
			sghmc.NAcc++

			// Write a sample to the channel.
			samples <- x
		}
	}()
}

// setDefaults sets the default value for auxiliary parameters.
func (mahmc *SgHMC) setDefaults() {
	if mahmc.L == 0 {
		mahmc.L = 10
	}
}
