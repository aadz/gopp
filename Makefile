gopp: gopp.go config.go
	go build $+

install: gopp
	install -s -m 0555 -o root -g bin -T gopp /usr/local/sbin/gopp
