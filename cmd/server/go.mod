module spotiflac-server

go 1.23

require (
	github.com/google/uuid v1.6.0
	spotiflac v0.0.0
)

// Point at the local SpotiFLAC repo root (two levels up from cmd/server)
replace spotiflac => ../../
