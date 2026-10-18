// Package docscheck holds the tests that keep the user docs honest: the
// wording rule (master roadmap §2) in README.md, HOW-TO-OPEN.txt and
// docs/, every screenshot they show exists, and the v0.6 field checklist
// (v0.6 §7.4-7.6) has every item with its steps and a Date, OS and Result
// line. It has no code of its own; run it with go test ./tools/docscheck.
// netdiag never imports it.
package docscheck
