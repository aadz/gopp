gopp: gopp.go config.go
	go build $+
	strip gopp

install: gopp
	cp gopp /usr/local/sbin/gopp
