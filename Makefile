gopp: gopp.go config.go
	go build $+
	strip gopp
