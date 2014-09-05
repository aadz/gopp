/*
	GOPP - Postfix policy written in Go
	Configuration processing
	by aadz, 2014
*/

package main

import (
	"fmt"
	"hash/crc64"
	"io/ioutil"
	"log"
	"log/syslog"
	"os"
	"os/user"
	"strconv"
	str "strings"
	"syscall"
	"time"
)

// Global vars
var (
	_cfg = map[string]string{
		"debug":             "yes",
		"grey_listing":      "no",
		"grey_list_delay":   "300",
		"greylist_exceptions": "-none-",
		"grey_list_expire":  "14400",
		"grey_list_store":   "internal",
		"listen_ip":         "127.0.0.1",
		"listen_port":       "10033",
		"log":               "syslog",
		"memcached_servers": "127.0.0.1:11211",
		"stat_interval":     "0",
		"user":              "-none-",
	}
)

func applay_cfg(initial bool, new_cfg map[string]string) {
	// Set dubug log at first
	d, found := new_cfg["debug"]
	if found && _cfg["debug"] != d {
		switch d {
		case "yes":
			LOG_DEBUG = true
			_cfg["debug"] = d
		case "no":
			LOG_DEBUG = false
			_cfg["debug"] = d
		default:
			_log_debug(fmt.Sprintf("Unknown setting %v for parameter debug", d))
		}
	}
	_log_debug("Set configuration parameters")

	// Set regular log
	logfile_name, found := new_cfg["log"]
	if found == false {
		logfile_name = _cfg["log"]
	}

	if logfile_name == "syslog" {
		_syslog, err := syslog.New(syslog.LOG_INFO|syslog.LOG_MAIL, PROG_NAME)
		_check(&err)
		log.SetOutput(_syslog)
		log.SetFlags(0)
	} else {
		f, err := os.OpenFile(logfile_name, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0664)
		_check(&err)
		log.SetOutput(f)
	}

	for par, val := range new_cfg {
		if _cfg[par] != val {
			_log_debug("New configuration value:", par, val)
		}

		switch par {
		case "grey_listing":
			if _cfg[par] != val {
				_cfg[par] = val
			}
			switch val {
			case "yes":
				GREYLIST = true
			case "no":
				GREYLIST = false
			default:
				_log(fmt.Sprintf("unknown setting %v for parameter grey_listing", val))
			}
		case "grey_list_delay":
			sec, err := strconv.Atoi(val)
			if err != nil {
				_log(fmt.Sprintf("incorrect setting %v for parameter grey_list_delay", val))
			} else {
				_cfg[par] = val
				GREYLIST_DELAY = int64(sec)
			}
		case "grey_list_expire":
			sec, err := strconv.Atoi(val)
			if err != nil {
				_log(fmt.Sprintf("incorrect setting %v for parameter grey_list_expire", val))
			} else {
				_cfg[par] = val
				GREYLIST_EXPIRE = int64(sec)
			}
		case "grey_list_store":
			switch val {
			case "internal", "memcached":
				_cfg[par] = val
			default:
				_log_debug(fmt.Sprintf("Unknown setting %v for parameter grey_list_store", val))
			}
		case "listen_ip":
			_cfg[par] = val
		case "listen_port":
			_cfg[par] = val
		case "memcached_servers":
			if _cfg[par] != val {
				_cfg[par] = val
			}
		case "stat_interval":
			seconds, err := strconv.Atoi(val)
			if err != nil {
				_log("incorrect value for stat_interwal:", val)
				STAT_INTERVAL = 0
			} else {
				_cfg[par] = val
				STAT_INTERVAL = time.Duration(time.Duration(seconds) * time.Second)
				_log_debug("STAT_INTERVAL set to", seconds)
			}
		case "user":
			if _cfg["user"] != val && val != "-none-" {
				if initial {
					uid, err := strconv.Atoi(val)
					if err != nil {
						usr, err := user.Lookup(val)
						if err == nil {
							uid, err = strconv.Atoi(usr.Uid)
						} else {
							_log_debug("Cannot find UID for", val, ":", err)
						}
					}
					err = syscall.Setuid(uid)
					_check(&err)
					_log_debug("UID set to", uid)
				}
			}
			_cfg[par] = val
		}
	}

	if GREYLIST {
		//CRC64_TABLE = crc64.MakeTable(0x42F0E1EBA9EA3693)
		CRC64_TABLE = crc64.MakeTable(crc64.ECMA)

		if _cfg["grey_list_store"] == "memcached" {
			set_mc_client()
		} else if _cfg["grey_list_store"] == "internal" {
			go clean_grey_map()
		}
	}

	go _stat()
}

func parse_cfg_line(line string) (string, string) {
	l := str.Trim(line, " \t")

	// strip comments
	sharpidx := str.Index(l, "#")
	if sharpidx != -1 {
		l = l[:sharpidx]
	}
	if len(l) == 0 {
		return "", ""
	}

	// split by '=' and turn parameter mname to lower case
	pv := str.SplitN(l, "=", 2)
	par := str.ToLower(str.Trim(pv[0], " \t"))
	val := str.Trim(pv[1], "'\", \t")
	if len(par) == 0 || len(val) == 0 {
		_log_debug("Error in configuration line: %v", line)
		return "", ""
	}
	return par, val
}

func read_config() {
	var new_cfg = make(map[string]string)

	// Read configuraton file
	cfg, err := ioutil.ReadFile(_cfg_file_name)
	_check(&err)

	for ln, line := range str.Split(string(cfg), "\n") {
		par, val := parse_cfg_line(line)
		if len(par) == 0 {
			continue
		}

		_, parOK := _cfg[par]
		if parOK == true { // check if parameter is alowed
			//_log_debug(fmt.Sprintf("Got cfg param %v:%v", par, val))
			new_cfg[par] = val
		} else {
			_log_debug(fmt.Sprintf("Unknown configuration parameter `%v' on line %v",
				par, ln+1))
		}
	}

	applay_cfg(true, new_cfg)
	_log(fmt.Sprintf("%v %v started, configuration read from %v",
		PROG_NAME, VERSION, _cfg_file_name))
}
