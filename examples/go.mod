module github.com/PatchSimple/centrifuge-go/examples

go 1.16

replace github.com/PatchSimple/centrifuge-go => ../

require (
	github.com/PatchSimple/centrifuge-go v0.3.0
	github.com/golang-jwt/jwt v3.2.2+incompatible
	go.uber.org/ratelimit v0.2.0
)
