// Package conformance holds the black-box parity suite. Every oracle corpus
// vector (testdata/vectors) runs through the engine's exported API and is
// compared byte-for-byte with the reference output, except the vectors
// listed in testdata/expected: their comparators prove that only the
// documented difference occurs, and design changes are pinned by hash.
package conformance
