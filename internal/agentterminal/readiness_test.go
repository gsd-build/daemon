package agentterminal

import "testing"

func TestReadinessPattern(t *testing.T) {
	d, err := NewReadinessDetector(StartRequest{ReadyPattern: `ready on \d+`}, DefaultLimits())
	if err != nil {
		t.Fatalf("detector: %v", err)
	}
	readiness, _, _, advanced := d.Observe("server ready on 3000")
	if !advanced || readiness.State != ReadinessReady || readiness.Source != "pattern" || readiness.MatchedText != "ready on 3000" {
		t.Fatalf("readiness = %#v advanced=%v", readiness, advanced)
	}
}

func TestReadinessURLExtraction(t *testing.T) {
	d, err := NewReadinessDetector(StartRequest{}, DefaultLimits())
	if err != nil {
		t.Fatalf("detector: %v", err)
	}
	_, ports, urls, advanced := d.Observe("Local: http://localhost:3000/")
	if !advanced {
		t.Fatal("expected advanced")
	}
	if len(ports) != 1 || ports[0].Host != "127.0.0.1" || ports[0].Port != 3000 {
		t.Fatalf("ports = %#v", ports)
	}
	if len(urls) != 1 || urls[0] != "http://127.0.0.1:3000/" {
		t.Fatalf("urls = %#v", urls)
	}
}

func TestReadinessDetectsSplitURL(t *testing.T) {
	d, err := NewReadinessDetector(StartRequest{}, DefaultLimits())
	if err != nil {
		t.Fatalf("detector: %v", err)
	}
	if _, _, _, advanced := d.Observe("server at local"); advanced {
		t.Fatal("advanced before URL was complete")
	}
	readiness, ports, urls, advanced := d.Observe("host:3000")
	if !advanced || readiness.State != ReadinessReady {
		t.Fatalf("readiness = %#v advanced=%v", readiness, advanced)
	}
	if len(ports) != 1 || ports[0].Port != 3000 || len(urls) != 1 {
		t.Fatalf("ports=%#v urls=%#v", ports, urls)
	}
}

func TestReadinessHeuristic(t *testing.T) {
	d, err := NewReadinessDetector(StartRequest{}, DefaultLimits())
	if err != nil {
		t.Fatalf("detector: %v", err)
	}
	readiness, _, _, advanced := d.Observe("listening on http://127.0.0.1:3000")
	if !advanced || readiness.State != ReadinessReady || readiness.Source != "heuristic" {
		t.Fatalf("readiness = %#v advanced=%v", readiness, advanced)
	}
}

func TestReadinessExit(t *testing.T) {
	d, err := NewReadinessDetector(StartRequest{}, DefaultLimits())
	if err != nil {
		t.Fatalf("detector: %v", err)
	}
	ready, advanced := d.MarkExit(0)
	if !advanced || ready.State != ReadinessReady || ready.Source != "process_exit" {
		t.Fatalf("ready exit = %#v advanced=%v", ready, advanced)
	}

	d2, err := NewReadinessDetector(StartRequest{}, DefaultLimits())
	if err != nil {
		t.Fatalf("detector: %v", err)
	}
	failed, advanced := d2.MarkExit(1)
	if !advanced || failed.State != ReadinessFailed {
		t.Fatalf("failed exit = %#v advanced=%v", failed, advanced)
	}
}
