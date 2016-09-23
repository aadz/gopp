gopp: gopp.go config.go
	go build $+

install: gopp
	install -s -m 0555 -o root -g bin -T gopp /usr/local/sbin/gopp
	mkdir -p /usr/local/share/man/man8
	gzip -c gopp.8 > /usr/local/share/man/man8/gopp.8.gz
