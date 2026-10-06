package controller

import (
	"math"
	"testing"
	"time"
)

func TestModel(t *testing.T) {
	now := time.Unix(100000, 0)
	for _, n := range []int{1, 2, 3, 60} {
		cs := make([]Channel, n)
		for i := range cs {
			cs[i] = Channel{Interface: string(rune('A' + i)), Online: true, BaselineWeight: 1, Metric: 1}
		}
		w, err := Weights(cs, nil, now, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		sum := 0
		for _, v := range w {
			if v < 1 {
				t.Fatal("nonpositive")
			}
			sum += v
		}
		if sum != 1000 {
			t.Fatal(sum)
		}
	}
	cs := []Channel{{Interface: "a", Online: true, BaselineWeight: 1, Metric: 1}, {Interface: "b", Online: true, BaselineWeight: 1, Metric: 1}, {Interface: "c", Online: true, BaselineWeight: 1, Metric: 1}}
	samples := map[string]Sample{"a": {Speed: 80, Count: 2, At: now}, "b": {Speed: 40, Count: 2, At: now}, "c": {Speed: 10, Count: 2, At: now}}
	w, _ := Weights(cs, samples, now, time.Hour)
	if w[0] != 615 || w[1] != 308 || w[2] != 77 {
		t.Fatal(w)
	}
	delete(samples, "c")
	w, _ = Weights(cs, samples, now, time.Hour)
	if w[0] != 445 || w[1] != 222 || w[2] != 333 {
		t.Fatal(w)
	}
	samples["a"] = Sample{Speed: math.NaN(), Count: 2, At: now}
	samples["b"] = Sample{Speed: 0, Count: 2, At: now}
	w, _ = Weights(cs, samples, now, time.Hour)
	if w[0] != 334 || w[1] != 333 || w[2] != 333 {
		t.Fatal(w)
	}
	samples["a"] = Sample{Speed: 80, Count: 2, At: now.Add(-2 * time.Hour)}
	w, _ = Weights(cs, samples, now, time.Hour)
	if w[0] != 334 {
		t.Fatal(w)
	}
	cs[2].Metric = 2
	w, _ = Weights(cs, samples, now, time.Hour)
	if w[2] != 0 || w[0] != 500 {
		t.Fatal(w)
	}
	cs[1].Interface = "a"
	if _, err := Weights(cs, samples, now, time.Hour); err == nil {
		t.Fatal("duplicate accepted")
	}
}
func TestEMAAndApply(t *testing.T) {
	now := time.Unix(1000, 0)
	s := Sample{}
	s = Observe(s, 80, now, .25)
	if s.Count != 1 || s.Speed != 80 {
		t.Fatal(s)
	}
	s = Observe(s, 40, now, .25)
	if s.Speed != 70 || s.Count != 2 {
		t.Fatal(s)
	}
	if ShouldApply([]int{500, 500}, []int{540, 460}, now, time.Time{}, false, 5, time.Minute) {
		t.Fatal("hysteresis")
	}
	if !ShouldApply([]int{500, 500}, []int{560, 440}, now, time.Time{}, false, 5, time.Minute) {
		t.Fatal("change")
	}
	if ShouldApply([]int{500, 500}, []int{600, 400}, now, now.Add(-30*time.Second), true, 5, time.Minute) {
		t.Fatal("minimum interval")
	}
}
