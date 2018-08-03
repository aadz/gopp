/*
	GOPP - Postfix policy written in Go
	by aadz, 2014
*/

package main

import (
	"flag"
	"fmt"
	"hash/crc64"
	"io"
	"log"
	"log/syslog"
	"net"
	"os"
	"runtime"
	"strconv"
	str "strings"
	"sync"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
)

const (
	PROG_NAME             string        = "gopp"
	VERSION               string        = "v0.2.4-21-gfd5532f"
	DEFAULT_CFG_FNAME     string        = "/etc/postfix/gopp.cfg"
	DEFAULT_ACTION        string        = "DUNNO"
	GREYLIST_DEFER_ACTION string        = "DEFER_IF_PERMIT Greylisted for %v seconds please try again"
	GREYLIST_PREFIX       string        = "GrlstPlc"
	CLEANER_INTERVAL      time.Duration = 300 * time.Second
)

// Global vars
var (
	_cfg_file_name     string
	_conn_cnt          uint
	_hostname          string
	_go_routines_run   map[string]byte  = make(map[string]byte)
	_grey_map          map[uint64]int64 = make(map[uint64]int64)
	_mc                *memcache.Client
	_PID               int
	_requests_cnt      uint
	_requests_duration time.Duration = 0
	_syslog            *syslog.Writer
	CRC64_TABLE        *crc64.Table
	GREYLIST           bool          = false
	GREYLIST_DELAY     int64         = 300
	GREYLIST_EXPIRE    int64         = 14400
	LOG_DEBUG          bool          = true
	STAT_INTERVAL      time.Duration = 0
)

var (
	_mutex                 sync.Mutex
	_conn_cnt_mutex        sync.Mutex // connections counter
	_grey_map_mutex        sync.Mutex
	_memcache_mutex        sync.Mutex
	_requests_cntavg_mutex sync.Mutex // used for _requests_cnt and _requests_duration
)

func init() {
	var err error

	_PID = os.Getpid()
	_hostname, err = os.Hostname()
	if err != nil {
		_log_debug("Cannot find the host name, 'localhost' assumed")
		_hostname = "localhost"
	}

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %v -c CONFIG_FILE\n\n", PROG_NAME)
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "  -h\tShow this help page.\n")
	}
	command_line_get()
	read_config()
}

func main() {
	laddr := _cfg["listen_ip"] + ":" + _cfg["listen_port"]
	l, err := net.Listen("tcp", laddr)
	_check(&err)    // or die
	defer l.Close() // Close the listener when the application closes.

	if _cfg["grey_listing"] == "yes" {
		_local_ip_addrs = get_local_ips()
	}
	_log_debug("listening on " + laddr)

	for {
		conn, err := l.Accept()
		_check(&err) // or die
		_log("connect from ", conn.RemoteAddr())
		_conn_cnt_mutex.Lock()
		_conn_cnt++
		_conn_cnt_mutex.Unlock()
		go handle_requests(conn)
	}
}

func check_grey(reqMap map[string]string) string {
	var client_address = reqMap["client_address"]
	var recipient = reqMap["recipient"]
	var sender = reqMap["sender"]

	// Skip checking if client has an IP address local for our host
	if _local_ip_addrs[client_address] {
		return DEFAULT_ACTION
	}

	msg_key := crc64.Checksum([]byte(str.ToLower(sender+recipient)+client_address),
		CRC64_TABLE)

	if LOG_DEBUG {
		qid := "" // Queue ID can be empty in policy request, log it if presented.
		if len(reqMap["queue_id"]) > 0 {
			qid = reqMap["queue_id"] + ": "
		}
		_log(fmt.Sprintf(
			"%vgrey list check: client %v, sender %v, recipient %v, checksum %x",
			qid, client_address, sender, recipient, msg_key))
	}

	switch _cfg["grey_list_store"] {
	case "internal":
		return check_grey_internal(msg_key)
	case "memcached":
		return check_grey_memcached(fmt.Sprintf("%v%x", GREYLIST_PREFIX, msg_key))
	}
	panic(fmt.Errorf("Unknown greylist storage `%v'", _cfg["grey_list_store"]))
}

func check_grey_internal(key uint64) string {
	now := int64(time.Now().Unix())
	var action string
	var delta int64

	_grey_map_mutex.Lock()
	try_time, found := _grey_map[key]
	_grey_map_mutex.Unlock()
	if found {
		// message key is already seen, so check time the key was added
		delta = now - try_time
		if delta > GREYLIST_EXPIRE {
			found = false
		} else if delta > GREYLIST_DELAY {
			action = DEFAULT_ACTION
		}
		if LOG_DEBUG {
			_log_debug(fmt.Sprintf("now:%v, try_time:%v, GREYLIST_DELAY:%v, delta:%v",
				now, try_time, GREYLIST_DELAY, delta))
		}
	}
	if !found {
		_grey_map_mutex.Lock()
		_grey_map[key] = now
		_grey_map_mutex.Unlock()
		delta = 0
	}
	wait_time := GREYLIST_DELAY - delta
	if action != DEFAULT_ACTION && wait_time > 0 {
		action = fmt.Sprintf(GREYLIST_DEFER_ACTION, wait_time)
	}
	return action
}

func check_grey_memcached(key string) string {
	now := time.Now().Unix()
	var action string
	var delta int64

	it := mc_get(key)
	_log_debug(fmt.Sprintf("Got from memcache: %v", it))

	if it == nil {
		if !mc_set(key, strconv.FormatInt(now, 10), GREYLIST_EXPIRE) {
			_log("cannot set memcache item")
			action = DEFAULT_ACTION
		}
		delta = GREYLIST_DELAY
	} else {
		_log_debug(fmt.Sprintf("Got memcache item: Key:%v, Value:%v (%v)",
			it.Key, it.Value, string(it.Value)))
		try_time, err := strconv.ParseInt(string(it.Value), 10, 0)

		if err != nil {
			_log(fmt.Sprintf("cannot convert %v to int: %v", it.Value, err))
			action = DEFAULT_ACTION
		}

		delta = GREYLIST_DELAY - (now - try_time)
		if delta <= 0 {
			action = DEFAULT_ACTION
		}
		_log_debug(fmt.Sprintf("now:%v, try_time:%v, GREYLIST_DELAY:%v, delta:%v",
			now, try_time, GREYLIST_DELAY, delta))
	}

	if action != DEFAULT_ACTION {
		action = fmt.Sprintf(GREYLIST_DEFER_ACTION, delta)
	}
	return action
}

func check_RCPT(rMap map[string]string) string {
	_log_debug("Check on RCPT state")

	if GREYLIST {
		res := check_grey(rMap)
		if res != DEFAULT_ACTION {
			return res
		}
	}
	return DEFAULT_ACTION
}

// GOROUTINE: creates and then periodically checks internal grey list
func clean_grey_map() {
	_mutex.Lock()
	_, found := _go_routines_run["clean_grey_map"]
	_mutex.Unlock()
	if found { // already run
		return
	} else {
		_mutex.Lock()
		_go_routines_run["clean_grey_map"] = 1
		_mutex.Unlock()
		_log_debug("Starting _grey_map cleaner")
	}

	for {
		if _cfg["grey_list_store"] != "internal" {
			// make greylist map empty
			_grey_map_mutex.Lock()
			_grey_map = make(map[uint64]int64)
			_grey_map_mutex.Unlock()

			_mutex.Lock()
			delete(_go_routines_run, "clean_grey_map")
			_mutex.Unlock()

			return
		}

		time.Sleep(CLEANER_INTERVAL)

		now := time.Now().Unix()
		deleted := 0

		_grey_map_mutex.Lock()
		start_time := time.Now()
		for key, val := range _grey_map {
			if now-val > GREYLIST_EXPIRE {
				delete(_grey_map, key)
				deleted++
			}
		}
		_grey_map_mutex.Unlock()
		_log(fmt.Sprintf("internal greylist cleaner: %v greylist entries deleted in %v", deleted, time.Now().Sub(start_time)))
	}
}

// Get command line parameters
func command_line_get() {
	flag.StringVar(&_cfg_file_name, "c", DEFAULT_CFG_FNAME, "Set configuration file name.")
	flagShortVersion := flag.Bool("v", false, "Show version information and exit.")
	flagVersion := flag.Bool("V", false, "Show version information of the programm and Go runtime, then exit.")
	flag.Parse()

	if *flagVersion {
		fmt.Println(PROG_NAME, VERSION, runtime.Version())
		os.Exit(0)
	}
	if *flagShortVersion {
		fmt.Println(PROG_NAME, VERSION)
		os.Exit(0)
	}
}

func handle_requests(conn net.Conn) {
	var start_time time.Time
	reqStr := ""     // Postfix request as a string
	EOF := false     // true indicates closed connection
	request_cnt := 0 // request counter for current connection only
	channel := make(chan string)
	defer close(channel)
	defer conn.Close()

	go read_request(conn, channel)
	for { // process incomming policy requests while not EOF
		if EOF {
			break
		}
		for {
			in_str := <-channel
			if in_str == "EOF" {
				EOF = true
				break
			}
			reqStr += in_str

			// if we've got the last part of a Postfix policy request here
			if str.HasSuffix(reqStr, "\n\n") {
				request_cnt++
				if LOG_DEBUG {
					_log_debug(fmt.Sprintf("Policy request from %v %v (%v bytes)",
						conn.RemoteAddr(), request_cnt, len(reqStr)))
					_log_debug(reqStr)
				}
				if STAT_INTERVAL > 0 {
					start_time = time.Now()
				}
				msg_map := parse_request(&reqStr)
				if len(msg_map) > 0 {
					conn.Write([]byte(fmt.Sprintf("action=%v\n\n", policy_check(msg_map))))
				}
				if STAT_INTERVAL > 0 { // update global counters
					d := time.Now().Sub(start_time)
					_requests_cntavg_mutex.Lock()
					_requests_duration += d
					_requests_cnt++
					_requests_cntavg_mutex.Unlock()
				}
				reqStr = "" // clean reqStr to get new request
			}
		}
	}
	_log(fmt.Sprintf("connection closed from %v after %v req sent",
		conn.RemoteAddr(), request_cnt))
}

func mc_get(key string) *memcache.Item {
	var it *memcache.Item
	_memcache_mutex.Lock()
	it, err := _mc.Get(key)
	_memcache_mutex.Unlock()
	if err != nil && err != memcache.ErrCacheMiss {
		_log_debug(err)
	}
	return it
}

func mc_set(key string, val string, exp int64) bool {
	it := memcache.Item{Key: key, Value: []byte(val), Expiration: int32(exp)}
	_log_debug("mc_set(): new memcache item:", it)

	_memcache_mutex.Lock()
	err := _mc.Set(&it)
	_memcache_mutex.Unlock()
	if err != nil {
		_log(err)
		return false
	}
	return true
}

func parse_request(pMsg *string) map[string]string {
	req := make(map[string]string)

	for _, line := range str.Split(*pMsg, "\n") {
		if len(line) == 0 {
			break // the end of request
		}
		pv := str.SplitN(line, "=", 2)
		req[pv[0]] = pv[1]
	}
	return req
}

func policy_check(rMap map[string]string) string {
	req_type, ok := rMap["request"]
	if ok == false || req_type != "smtpd_access_policy" {
		_log("policy request type unknown")
		return ""
	}

	action := DEFAULT_ACTION

	switch rMap["protocol_state"] {
	case "RCPT":
		action = check_RCPT(rMap)
	default:
		_log("unknown or unsuported protocol state ", rMap["protocol_state"])
	}
	return action
}

func read_request(conn net.Conn, channel chan string) {
	for {
		buf := make([]byte, 768) // should be enouhg for usual request
		cnt, err := conn.Read(buf)
		if err == io.EOF { // connection closed by client
			channel <- "EOF"
			return
		} else if err != nil {
			_log(fmt.Sprintf("error reading from %v: %v",
				conn.RemoteAddr(), err.Error()))
		}
		channel <- string(buf[0:cnt])
	}
}

func set_mc_client() {
	// define new memcached client
	srv := str.Split(_cfg["memcached_servers"], ",")
	for i, s := range srv {
		srv[i] = str.Trim(s, " \t")
	}
	_mc = memcache.New(srv...)
}

func _check(e *error) {
	if *e != nil {
		_log("Fatal:", *e)
		fmt.Println("Fatal:", *e)
		os.Exit(1)
	}
}

func _log(v ...interface{}) {
	log.Print(v...)
	if LOG_DEBUG {
		_log_debug(v...)
	}
}

func _log_debug(v ...interface{}) {
	if LOG_DEBUG {
		fmt.Printf("%v %v %v[%v]: ", _now(), _hostname, PROG_NAME, _PID)
		fmt.Println(v...)
	}
}

func _now() string {
	return time.Now().Format(time.StampMilli)
}

func _stat() {
	_mutex.Lock()
	_, found := _go_routines_run["_stat"]
	_mutex.Unlock()
	if found || STAT_INTERVAL <= 0 { // already run or need no statistics
		return
	} else { // registration
		_mutex.Lock()
		_go_routines_run["_stat"] = 1
		_mutex.Unlock()
	}
	_log_debug("Stats collector run")

	var (
		stat_timer                              *time.Timer = time.NewTimer(STAT_INTERVAL)
		conn_cnt, requests_cnt                  uint
		requests_duration, request_avg_duration time.Duration
		str_grey_map_cnt                        string
	)

	prev_ts := time.Now() // timestamp
	for {
		_ = <-stat_timer.C
		ts := time.Now() // timestamp
		stat_timer.Reset(STAT_INTERVAL)
		interval := float32(ts.Sub(prev_ts) / time.Second)
		prev_ts = ts

		// Connections counter
		_conn_cnt_mutex.Lock()
		conn_cnt = _conn_cnt
		_conn_cnt = 0
		_conn_cnt_mutex.Unlock()

		// Requests counter & Average request duration
		_requests_cntavg_mutex.Lock()
		requests_cnt = _requests_cnt
		_requests_cnt = 0
		requests_duration = _requests_duration
		_requests_duration = 0
		_requests_cntavg_mutex.Unlock()

		if requests_cnt > 0 {
			request_avg_duration = requests_duration / time.Duration(requests_cnt)
		} else {
			request_avg_duration = 0
		}

		// Internal grey list records count
		if GREYLIST && _cfg["grey_list_store"] == "internal" {
			_grey_map_mutex.Lock()
			greylisted := len(_grey_map)
			_grey_map_mutex.Unlock()
			str_grey_map_cnt = fmt.Sprintf(", greylisted %v", greylisted)
		}

		_log(fmt.Sprintf("statistics: interval %v, connections %v (%.4f p/s), requests %v (%.4f p/s, %v avg p/req)%v",
			STAT_INTERVAL, conn_cnt, float32(conn_cnt)/interval, requests_cnt,
			float32(requests_cnt)/interval, request_avg_duration, str_grey_map_cnt))
	}
}
