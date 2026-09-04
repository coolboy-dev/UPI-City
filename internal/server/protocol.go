// Package server runs the simulation in real time and streams it to a browser.
//
// Transport is Server-Sent Events over plain net/http, with a POST endpoint
// for control. The plan called for WebSockets; SSE was chosen instead because
// the only thing WebSockets would add here is a client→server channel, and
// that channel is one POST handler. In exchange the binary keeps zero external
// dependencies and there is no handshake to debug during a live demo.
package server

import "github.com/yug/upi-city/internal/obs"

// Frame kinds on the wire.
const (
	KindSnapshot = "snapshot"
	KindDelta    = "delta"
)

// Node is one agent's fixed position and identity.
//
// Sent once, in the snapshot. Agents do not move, so the layout is computed
// once server-side and never recomputed — a continuously re-simulated force
// layout would burn a core to produce a graph that jitters differently on
// every reload and every take of the demo video.
type Node struct {
	ID   obs.AgentID `json:"i"`
	X    float32     `json:"x"`
	Y    float32     `json:"y"`
	R    float32     `json:"r"`
	Kind string      `json:"k"`
	Bank uint8       `json:"b"`
}

// Snapshot is the full picture, sent on connect and periodically to resync a
// client that dropped frames.
type Snapshot struct {
	Kind  string   `json:"kind"`
	Tick  obs.Tick `json:"tick"`
	Nodes []Node   `json:"nodes"`
	Banks []string `json:"banks"`
}

// Tx is one settled transaction, packed as an array to keep frames small.
//
// Layout: [from, to, amountPaise, status, score×1000, truthLabel, rival×1000]
//
// The truth label rides along ONLY so the interface can offer a "reveal
// ground truth" toggle. It is attached here, after detection has already run,
// and no detector ever sees this struct. Being able to show the audience the
// two false positives and the one miss, live, is the most persuasive thing
// this project can put on screen — but it has to be obvious that the label is
// a display concern and not an input.
//
// The seventh slot is a competing detector's score for the same payment,
// computed by a program outside this repository from a file containing no
// labels. It is zero when no challenger has been loaded. Carrying both scores
// on one transaction is what lets the interface colour a payment by WHO caught
// it rather than merely whether something did — the two systems are looking at
// the identical payment at the identical moment, which is the entire claim.
type Tx [7]int64

// Flag is a scored agent: [agentID, score×1000].
type Flag [2]int64

// Live is the running confusion matrix, computed server-side against ground
// truth as the simulation runs.
type Live struct {
	Tick       obs.Tick `json:"tick"`
	TPS        float64  `json:"tps"`
	Settled    int      `json:"settled"`
	Fraudulent int      `json:"fraud"`

	TP int `json:"tp"`
	FP int `json:"fp"`
	FN int `json:"fn"`

	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	FPPer1k   float64 `json:"fp1k"`

	Threshold float64 `json:"tau"`
	Speed     float64 `json:"speed"`
	Paused    bool    `json:"paused"`
	Dropped   int     `json:"dropped"`
	// Replay marks a recorded run, so the interface can say so plainly
	// instead of implying a live network.
	Replay   bool `json:"replay"`
	Finished bool `json:"finished"`

	// ── Head to head, when a challenger's scores have been loaded ─────────
	//
	// Counted server-side against the same threshold and the same ground
	// truth, so the two detectors differ in exactly one thing: the score.
	Rival string `json:"rival,omitempty"`
	// RivalTau is the challenger's threshold, chosen so it flags the same
	// SHARE of traffic this project's threshold does. Sent to the browser so
	// the particle colours and these counters agree on who caught what.
	RivalTau float64 `json:"rivalTau"`
	// BothCaught, MineOnly and RivalOnly partition the flagged fraud;
	// BothMissed is fraud neither detector reached, which is the number the
	// head-to-head view exists to make visible.
	BothCaught int `json:"bothCaught"`
	MineOnly   int `json:"mineOnly"`
	RivalOnly  int `json:"rivalOnly"`
	BothMissed int `json:"bothMissed"`
	// RivalFP is the challenger's false positives, so a viewer can see what
	// its extra catches cost rather than only what they bought.
	RivalFP int `json:"rivalFP"`

	// The decision layer. Blocking and reviewing are different actions with
	// very different costs, so they are counted separately: block precision
	// is what protects customers, review rate is what the queue can absorb.
	TauBlock       float64 `json:"tauBlock"`
	Blocked        int     `json:"blocked"`
	Reviewed       int     `json:"reviewed"`
	FraudBlocked   int     `json:"fraudBlocked"`
	FraudReviewed  int     `json:"fraudReviewed"`
	BlockPrecision float64 `json:"blockPrecision"`
	ReviewRate     float64 `json:"reviewRate"`
	FalseBlocked   int     `json:"falseBlocked"`
}

// Incident is a live scenario in progress.
type Incident struct {
	ID        uint32  `json:"id"`
	Kind      string  `json:"kind"`
	StartTick uint64  `json:"start"`
	Members   int     `json:"members"`
	Detected  bool    `json:"detected"`
	LatencyS  float64 `json:"latency"`
	Intensity float64 `json:"intensity"`

	// Narrative is the audit-trail explanation. Source is shown as a badge
	// so a reader can always tell generated prose from a filled-in template.
	Headline  string `json:"headline,omitempty"`
	Narrative string `json:"narrative,omitempty"`
	Action    string `json:"action,omitempty"`
	Source    string `json:"source,omitempty"`
}

// Delta is one batch of activity, emitted at a fixed rate rather than per
// transaction. A frame per transaction would flood the browser at several
// hundred messages a second and stall the render loop.
type Delta struct {
	Kind      string     `json:"kind"`
	Tick      obs.Tick   `json:"tick"`
	Tx        []Tx       `json:"tx"`
	Flags     []Flag     `json:"flags"`
	Live      Live       `json:"live"`
	Incidents []Incident `json:"incidents"`
}

// Command is a control message from the browser.
type Command struct {
	Action    string  `json:"action"`    // pause, resume, speed, inject, threshold, reset
	Value     float64 `json:"value"`     // speed multiplier or threshold
	Scenario  string  `json:"scenario"`  // for inject
	RingSize  int     `json:"ringSize"`  // severity
	Victims   int     `json:"victims"`   // severity
	RampTicks uint64  `json:"rampTicks"` // severity
}
