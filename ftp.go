package main

import (
	"fmt"
	"log"
	"net"
	//"net/http"
	"strings"
	"time"
)

const (
	AUTH_SLEEP    = 1 // seconds to sleep in event of any auth failure
	FAILURE_LIMIT = 3 // max number of failures in a connection session
)

type FtpSession struct {
	Host               string
	Authenticated      bool
	Username           string
	Failures           int
	Upload_requested   bool
	Download_requested bool
	Passive_mode       bool
	Data_port          int
}

// We have a finite pool of ports available for data connections that must be
// shared among all goroutines (each goroutine is one FTP session). Two goroutines
// may not use the same port at the same time, so there must be a way of "reserving"
// and "releasing" a port that is threadsafe across goroutines
//
// PortProvider listens on input channel (i) for a message:
// 	   { 0, 0 }       request a port
//     { 1, 12345 }   release port 12345 back into pool
// For requests, it writes the reserved port to the output channel or -1 for error or no available ports
// For releases, no response is given

type port_request struct {
	action int
	port   int
}

//Passive port provider channels
var pp_input chan port_request
var pp_output chan int

//Active port provider channels
var ap_input chan port_request
var ap_output chan int

func PortProvider(port_start int, port_end int, i <-chan port_request, o chan<- int) {
	available := make(map[int]int)
	reserved := make(map[int]int)

	if port_start > port_end {
		log.Fatalf("ERROR: PortProvider: port_start (%v) > port_end (%v)!\n", port_start, port_end)
	}

	// populate available ports
	for i := 0; (port_start + i) <= port_end; i++ {
		available[port_start+i] = 0
	}

	for {
		req := <-i
		if req.action == 0 { // Request available port
			new_port := 0
			for k, _ := range available {
				new_port = k
			}
			if new_port == 0 { //No available ports
				o <- -1
			} else {
				reserved[new_port] = 0
				delete(available, new_port)
				o <- new_port
			}
		} else if req.action == 1 { // Release port back to pool
			if req.port < port_start || req.port > port_end {
				log.Printf("WARNING: PortProvider: release request for port number that's out of range! %v\n", req.port)
				continue
			}
			if _, ok := reserved[req.port]; !ok {
				log.Printf("WARNING: PortProvider: release request for port that is not reserved! %v\n", req.port)
				continue
			}
			delete(reserved, req.port)
			available[req.port] = 0
		} else {
			log.Printf("WARNING: PortProvider: bad action! %v\n", req.action)
			continue
		}
	}
}

func FtpCommand(conn net.Conn, sess *FtpSession, cmd_str string) bool {
	cmd_str = strings.TrimSpace(cmd_str)
	cmd_slice := strings.Split(cmd_str, " ")
	cmd := strings.ToUpper(cmd_slice[0])
	switch cmd {
	case "QUIT":
		WriteResponse(conn, 221, "Goodbye")
		return false
	/* User Authentication commands */
	case "USER":
		ok, code, msg := AuthUser(cmd_slice, sess)
		if !ok {
			sess.Failures += 1
			time.Sleep(time.Duration(AUTH_SLEEP) * time.Second)
		}
		WriteResponse(conn, code, msg)
	case "PASS":
		ok, code, msg := CheckPassword(cmd_slice, sess)
		if !ok {
			sess.Failures += 1
			time.Sleep(time.Duration(AUTH_SLEEP) * time.Second)
		}
		WriteResponse(conn, code, msg)
	case "REIN":
		sess.Authenticated = false
		sess.Username = ""
		WriteResponse(conn, 200, "OK Session Reinitialized")
	/* File I/O commands */
	case "RETR":
	case "PWD":
		if sess.Authenticated {
			WriteResponse(conn, 200, Configuration.Fakedir_root)
		} else {
			WriteResponse(conn, 500, "Not logged in")
		}
	case "TYPE":
		if sess.Authenticated {
			_, code, msg := TypeCommand(cmd_slice, sess)
			WriteResponse(conn, code, msg)
		} else {
			WriteResponse(conn, 500, "Not logged in")
		}
	case "LIST":
		if sess.Authenticated {
			WriteResponseMultiLine(conn, 200, Configuration.Fakedir_list)
		} else {
			WriteResponse(conn, 500, "Not logged in")
		}
	/* System/Formatting Commands */
	case "FEAT":
		if sess.Authenticated {
			features := []string{
				"Features:",
				"UTF8",
				"PASV",
				"REST STREAM",
				"End",
			}
			WriteResponseMultiLine(conn, 211, features)
		} else {
			WriteResponse(conn, 500, "Not logged in")
		}
	case "STRU":
		if sess.Authenticated {
			_, code, msg := StructureCommand(cmd_slice, sess)
			WriteResponse(conn, code, msg)
		} else {
			WriteResponse(conn, 500, "Not logged in")
		}
	case "SYST":
		if sess.Authenticated {
			WriteResponse(conn, 215, "UNIX")
		} else {
			WriteResponse(conn, 500, "Not logged in")
		}
	case "STAT":
		if sess.Authenticated {
			WriteResponse(conn, 200, "Rad. Yourself?")
		} else {
			WriteResponse(conn, 500, "Not logged in")
		}
	case "HELP":
		WriteResponse(conn, 200, Configuration.Help)
	case "NOOP":
		WriteResponse(conn, 200, "OK")
	default:
		if Configuration.Permissive {
			WriteResponse(conn, 200, "OK (not supported but faking it)")
		} else {
			WriteResponse(conn, 500, "Bad Command")
		}
	}
	if sess.Failures >= FAILURE_LIMIT {
		return false
	}
	return true
}

func WriteResponse(conn net.Conn, code int, msg string) {
	conn.Write([]byte(fmt.Sprintf("%v %v\r\n", code, msg)))
}

// FTP spec requires multiline responses to begin with: {CODE}-Line1<CRLF>
// Line2<CRLF>
// ...
// and end with: {CODE} LastLine
func WriteResponseMultiLine(conn net.Conn, code int, msg_slice []string) {
	msg_slice[0] = fmt.Sprintf("%v-%v", code, msg_slice[0])
	msg_slice[len(msg_slice)-1] = fmt.Sprintf("%v %v", code, msg_slice[len(msg_slice)-1])
	msg_string := strings.Join(msg_slice, "\r\n")
	conn.Write([]byte(fmt.Sprintf("%v\r\n", msg_string)))
}

func AuthUser(user_cmd []string, sess *FtpSession) (bool, int, string) {
	if len(user_cmd) != 2 {
		return false, 500, "Bad USER command"
	}
	if sess.Authenticated {
		return false, 500, "Already logged in"
	}
	if _, ok := UserCredentials[user_cmd[1]]; !ok {
		return false, 530, "Bad or Unknown User"
	}
	sess.Username = user_cmd[1]
	sess.Authenticated = false
	return true, 331, "User OK, specify password"
}

func CheckPassword(pass_cmd []string, sess *FtpSession) (bool, int, string) {
	if len(pass_cmd) != 2 {
		return false, 500, "Bad PASS command"
	}
	if sess.Authenticated {
		return false, 500, "Already logged in"
	}
	if sess.Username != "" {
		if pw, ok := UserCredentials[sess.Username]; ok {
			if pass_cmd[1] == pw {
				sess.Authenticated = true
				return true, 230, "OK (login successful)"
			} else {
				sess.Authenticated = false
				return false, 530, "Bad Password"
			}
		} else {
			return false, 530, "Bad User"
		}
	} else {
		return false, 530, "Not logged in (need username)"
	}
}

func RetrieveFile(retr_cmd []string, sess *FtpSession) (bool, int, string) {
	if len(retr_cmd) != 2 {
		return false, 500, "Bad RETR command"
	}
	if !sess.Authenticated {
		return false, 530, "Not logged in"
	}
	if Configuration.Pull.Url != "" {
		// client := &http.Client{
		// 	CheckRedirect: redirectPolicyFunc,
		// }
		// req, err := http.NewRequest("GET", Configuration.Pull.Url, nil)
		// if err != nil {
		// 	log.Printf("Error creating pull request: %v\n", err)
		// }
		// for h := range Configuration.Pull.Headers {
		// 	for k, v := range h {
		// 		req.Header.Add(k, v)
		// 	}
		// }
		// resp, err := client.Do(req)
		// if err != nil {
		// 	log.Printf("Error in PULL! %v\n", err)
		// 	return false, 500, "Internal Error"
		// }
		// defer resp.Body.Close()

	}
	return false, 500, "error"
}

func StructureCommand(stru_cmd []string, sess *FtpSession) (bool, int, string) {
	if len(stru_cmd) != 2 {
		return false, 500, "Bad STRU command"
	}
	if stru_cmd[1] != "FILE" {
		return false, 510, "Only FILE structure supported"
	}
	return true, 211, "OK"
}

func TypeCommand(type_cmd []string, sess *FtpSession) (bool, int, string) {
	if len(type_cmd) != 2 {
		return false, 500, "Bad TYPE command"
	}
	if type_cmd[1] != "I" {
		return false, 510, "Only binary mode is supported (I)"
	}
	return true, 200, "OK (Binary mode)"
}
