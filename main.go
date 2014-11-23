package main

import (
	"bufio"
	"fmt"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"log"
	"net"
	"os"
	"strings"
)

const (
	CONFIG_FILE = "config.yaml"
	NAME        = "FakeTP"
	VERSION     = "0.1.0"
)

var UserCredentials map[string]string

type UserAuth struct {
	File     string      `yaml:"file,omitempty"`
	Endpoint ApiEndpoint `yaml:"endpoint,omitempty"`
}

type ApiEndpoint struct {
	Url       string
	Params    []map[string]string
	Headers   []map[string]string
	Body_name string
}

type ConfigOptions struct {
	User_auth               UserAuth
	Push                    ApiEndpoint
	Pull                    ApiEndpoint `yaml:"pull,omitempty"`
	Permissive              bool        `yaml:"permissive,omitempty"`
	Address                 string
	Insecure_port           string
	Secure_port             string
	Motd                    string
	Help                    string
	Fakedir_root            string
	Fakedir_list            []string
	Data_ports              map[string]int
	Strict_active_mode      bool
	Promiscuous_active_mode bool
}

var Configuration ConfigOptions

// Read user credentials file. Expected to be one account per line, username and password (in that order) separated by exactly one space
func ReadUserCredentialFile(filename string) bool {
	UserCredentials = make(map[string]string)
	if filename == "" {
		log.Fatalf("ERROR: only file-based authentication supported for now\n")
	}
	inputf, err := os.Open(filename)
	if err != nil {
		log.Fatalf("Error opening user auth file: %v\n", err)
		return false
	}
	defer inputf.Close()

	scanner := bufio.NewScanner(inputf)
	i := 0
	for i = 0; scanner.Scan(); i++ {
		line := scanner.Text()
		line_slice := strings.Split(line, " ")
		if len(line_slice) != 2 {
			log.Printf("Bad user credential line (%v): %v; skipped\n", i, line)
		}
		UserCredentials[line_slice[0]] = line_slice[1]
	}
	log.Printf("%v user credentials read from %v\n", i, filename)
	return true
}

func main() {

	config_bytes, err := ioutil.ReadFile(CONFIG_FILE)
	if err != nil {
		log.Fatalf("Error reading config file: %v\n", err)
	}

	err = yaml.Unmarshal(config_bytes, &Configuration)
	if err != nil {
		log.Fatalf("Error parsing config: %v\n", err)
	}

	//Validate data port list
	params := []string{"begin", "end"}
	for p := range params {
		param := params[p]
		if val, ok := Configuration.Data_ports[param]; ok {
			if val > 65535 || val < 1024 {
				log.Fatalf("Bad data_ports value: %v: %v\n", param, val)
			}
		} else {
			log.Fatalf("Missing data_ports option: %v\n", param)
		}
	}

	if Configuration.Data_ports["begin"] > Configuration.Data_ports["end"] {
		log.Fatalf("Bad data_ports values (begin must be <= end)\n")
	}

	if !ReadUserCredentialFile(Configuration.User_auth.File) {
		return
	}

	pp_input = make(chan port_request)
	pp_output = make(chan int)

	//Passive ports
	go PortProvider(Configuration.Data_ports["begin"], Configuration.Data_ports["end"], pp_input, pp_output)
	//Active ports
	if Configuration.Strict_active_mode {
		ap_input = make(chan port_request)
		ap_output = make(chan int)
		go PortProvider(20, 20, ap_input, ap_output)
	} else {
		ap_input = pp_input
		ap_output = pp_output
	}

	conn_string := fmt.Sprintf("%v:%v", Configuration.Address, Configuration.Insecure_port)
	s, err := net.Listen("tcp", conn_string)
	if err != nil {
		log.Fatalf("Error opening socket: %v\n", err)
	}
	defer s.Close()

	log.Printf("Listening on: %v\n", conn_string)

	for {
		conn, err := s.Accept()
		if err != nil {
			log.Printf("Error accepting tcp connection!\n")
		} else {
			var session FtpSession
			session.Authenticated = false
			session.Failures = 0
			session.Host = conn.RemoteAddr().String()
			go NewConnection(conn, &session)
		}
	}

}

func ListenControlChannel() {

}

func ListenDataChannel() {

}

func NewConnection(conn net.Conn, sess *FtpSession) {
	conn.Write([]byte(fmt.Sprintf("%v (%v)\n", NAME, VERSION)))
	conn.Write([]byte(fmt.Sprintf("%v\n", Configuration.Motd)))
	connbuf := bufio.NewReader(conn)
	defer conn.Close()
	for {
		str, err := connbuf.ReadString('\n')
		if err != nil {
			log.Printf("ERROR: bad command: %v\n", err)
			continue
		}
		if !FtpCommand(conn, sess, str) {
			break
		}
	}
}
