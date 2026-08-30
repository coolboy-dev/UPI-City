package metrics

// IntensityBucket is detector performance against attacks of a given strength.
type IntensityBucket struct {
	Lo     float64 `json:"lo"`
	Hi     float64 `json:"hi"`
	Fraud  int     `json:"fraud_tx"`
	Caught int     `json:"caught"`
	Recall float64 `json:"recall"`
}

// RecallByIntensity answers the question a ramped attack exists to ask:
// how strong does an attack have to be before this detector notices it?
//
// A single recall figure averages over the whole attack, including the loud
// part at full strength, and so overstates what the detector would do against
// an adversary who simply operates more quietly. Bucketing by the attack's own
// intensity exposes the knee — the point below which the ring is effectively
// invisible — and that knee is the honest sensitivity claim.
//
// This is only computable because the scenario ramps and because each
// transaction records the intensity in force when it was created. With
// instant-on fraud every row would sit in the top bucket and the curve would
// not exist.
func RecallByIntensity(rows Rows, tau float64, buckets int) []IntensityBucket {
	if buckets < 2 {
		buckets = 10
	}
	type acc struct{ fraud, caught int }
	bs := make([]acc, buckets)

	for _, r := range rows {
		if !r.Fraudulent() || r.Intensity <= 0 {
			continue
		}
		i := int(r.Intensity * float64(buckets))
		if i >= buckets {
			i = buckets - 1
		}
		if i < 0 {
			i = 0
		}
		bs[i].fraud++
		if r.Score >= tau {
			bs[i].caught++
		}
	}

	out := make([]IntensityBucket, 0, buckets)
	for i, a := range bs {
		b := IntensityBucket{
			Lo:     float64(i) / float64(buckets),
			Hi:     float64(i+1) / float64(buckets),
			Fraud:  a.fraud,
			Caught: a.caught,
		}
		if a.fraud > 0 {
			b.Recall = float64(a.caught) / float64(a.fraud)
		}
		out = append(out, b)
	}
	return out
}

// DetectionKnee is the lowest attack intensity at which recall first clears a
// floor — the quietest attack this detector reliably notices.
//
// Returns false when no bucket clears it, which is itself the result: the
// detector never becomes reliable at any strength tested.
func DetectionKnee(bs []IntensityBucket, floor float64) (float64, bool) {
	for _, b := range bs {
		if b.Fraud >= 20 && b.Recall >= floor {
			return b.Lo, true
		}
	}
	return 0, false
}
