package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"

	"github.com/dotcloud/docker/api"
	"github.com/dotcloud/docker/builtins"
	"github.com/dotcloud/docker/dockerversion"
	"github.com/dotcloud/docker/engine"
	"github.com/dotcloud/docker/opts"
	flag "github.com/dotcloud/docker/pkg/mflag"
	"github.com/dotcloud/docker/sysinit"
	"github.com/dotcloud/docker/utils"
)

const (
	defaultCaFile   = "ca.pem"
	defaultKeyFile  = "key.pem"
	defaultCertFile = "cert.pem"
)

var (
	dockerConfDir = os.Getenv("HOME") + "/.docker/"
)

func main() {
	if selfPath := utils.SelfPath(); strings.Contains(selfPath, ".dockerinit") {
		// Running in init mode
		sysinit.SysInit()
		return
	}

	var (
		flVersion            = flag.Bool([]string{"v", "-version"}, false, "Print version information and quit")
		flDaemon             = flag.Bool([]string{"d", "-daemon"}, false, "Enable daemon mode")
		flDebug              = flag.Bool([]string{"D", "-debug"}, false, "Enable debug mode")
		flAutoRestart        = flag.Bool([]string{"r", "-restart"}, true, "Restart previously running containers")
		bridgeName           = flag.String([]string{"b", "-bridge"}, "", "Attach containers to a pre-existing network bridge; use 'none' to disable container networking")
		bridgeIp             = flag.String([]string{"#bip", "-bip"}, "", "Use this CIDR notation address for the network bridge's IP, not compatible with -b")
		pidfile              = flag.String([]string{"p", "-pidfile"}, "/var/run/docker.pid", "Path to use for daemon PID file")
		flRoot               = flag.String([]string{"g", "-graph"}, "/var/lib/docker", "Path to use as the root of the docker runtime")
		flSocketGroup        = flag.String([]string{"G", "-group"}, "docker", "Group to assign the unix socket specified by -H when running in daemon mode; use '' (the empty string) to disable setting of a group")
		flEnableCors         = flag.Bool([]string{"#api-enable-cors", "-api-enable-cors"}, false, "Enable CORS headers in the remote API")
		flDns                = opts.NewListOpts(opts.ValidateIp4Address)
		flDnsSearch          = opts.NewListOpts(opts.ValidateDomain)
		flEnableIptables     = flag.Bool([]string{"#iptables", "-iptables"}, true, "Enable Docker's addition of iptables rules")
		flEnableIpForward    = flag.Bool([]string{"#ip-forward", "-ip-forward"}, true, "Enable net.ipv4.ip_forward")
		flDefaultIp          = flag.String([]string{"#ip", "-ip"}, "0.0.0.0", "Default IP address to use when binding container ports")
		flInterContainerComm = flag.Bool([]string{"#icc", "-icc"}, true, "Enable inter-container communication")
		flGraphDriver        = flag.String([]string{"s", "-storage-driver"}, "", "Force the docker runtime to use a specific storage driver")
		flExecDriver         = flag.String([]string{"e", "-exec-driver"}, "native", "Force the docker runtime to use a specific exec driver")
		flHosts              = opts.NewListOpts(api.ValidateHost)
		flMtu                = flag.Int([]string{"#mtu", "-mtu"}, 0, "Set the containers network MTU; if no value is provided: default to the default route MTU or 1500 if no default route is available")
		flTls                = flag.Bool([]string{"-tls"}, false, "Use TLS; implied by tls-verify flags")
		flTlsVerify          = flag.Bool([]string{"-tlsverify"}, false, "Use TLS and verify the remote (daemon: verify client, client: verify daemon)")
		flCa                 = flag.String([]string{"-tlscacert"}, dockerConfDir+defaultCaFile, "Trust only remotes providing a certificate signed by the CA given here")
		flCert               = flag.String([]string{"-tlscert"}, dockerConfDir+defaultCertFile, "Path to TLS certificate file")
		flKey                = flag.String([]string{"-tlskey"}, dockerConfDir+defaultKeyFile, "Path to TLS key file")
	)
	flag.Var(&flDns, []string{"#dns", "-dns"}, "Force docker to use specific DNS servers")
	flag.Var(&flDnsSearch, []string{"-dns-search"}, "Force Docker to use specific DNS search domains")
	flag.Var(&flHosts, []string{"H", "-host"}, "tcp://host:port, unix://path/to/socket, fd://* or fd://socketfd to use in daemon mode. Multiple sockets can be specified")

	flag.Parse()

	if *flVersion {
		showVersion()
		return
	}
	if flHosts.Len() == 0 {
		defaultHost := os.Getenv("DOCKER_HOST")

		if defaultHost == "" || *flDaemon {
			// If we do not have a host, default to unix socket
			defaultHost = fmt.Sprintf("unix://%s", api.DEFAULTUNIXSOCKET)
		}
		if _, err := api.ValidateHost(defaultHost); err != nil {
			log.Fatal(err)
		}
		flHosts.Set(defaultHost)
	}

	if *bridgeName != "" && *bridgeIp != "" {
		log.Fatal("You specified -b & --bip, mutually exclusive options. Please specify only one.")
	}

	if *flDebug {
		os.Setenv("DEBUG", "1")
	}

	if *flDaemon {
		if flag.NArg() != 0 {
			flag.Usage()
			return
		}

		// set up the TempDir to use a canonical path
		tmp := os.TempDir()
		realTmp, err := utils.ReadSymlinkedDirectory(tmp)
		if err != nil {
			log.Fatalf("Unable to get the full path to the TempDir (%s): %s", tmp, err)
		}
		os.Setenv("TMPDIR", realTmp)

		// get the canonical path to the Docker root directory
		root := *flRoot
		var realRoot string
		if _, err := os.Stat(root); err != nil && os.IsNotExist(err) {
			realRoot = root
		} else {
			realRoot, err = utils.ReadSymlinkedDirectory(root)
			if err != nil {
				log.Fatalf("Unable to get the full path to root (%s): %s", root, err)
			}
		}

		eng, err := engine.New(realRoot)
		if err != nil {
			log.Fatal(err)
		}
		// Load builtins
		builtins.Register(eng)
		// load the daemon in the background so we can immediately start
		// the http api so that connections don't fail while the daemon
		// is booting
		go func() {
			// Load plugin: httpapi
			job := eng.Job("initserver")
			job.Setenv("Pidfile", *pidfile)
			job.Setenv("Root", realRoot)
			job.SetenvBool("AutoRestart", *flAutoRestart)
			job.SetenvList("Dns", flDns.GetAll())
			job.SetenvList("DnsSearch", flDnsSearch.GetAll())
			job.SetenvBool("EnableIptables", *flEnableIptables)
			job.SetenvBool("EnableIpForward", *flEnableIpForward)
			job.Setenv("BridgeIface", *bridgeName)
			job.Setenv("BridgeIP", *bridgeIp)
			job.Setenv("DefaultIp", *flDefaultIp)
			job.SetenvBool("InterContainerCommunication", *flInterContainerComm)
			job.Setenv("GraphDriver", *flGraphDriver)
			job.Setenv("ExecDriver", *flExecDriver)
			job.SetenvInt("Mtu", *flMtu)
			if err := job.Run(); err != nil {
				log.Fatal(err)
			}
			// after the daemon is done setting up we can tell the api to start
			// accepting connections
			if err := eng.Job("acceptconnections").Run(); err != nil {
				log.Fatal(err)
			}
		}()

		// Serve api
		job := eng.Job("serveapi", flHosts.GetAll()...)
		job.SetenvBool("Logging", true)
		job.SetenvBool("EnableCors", *flEnableCors)
		job.Setenv("Version", dockerversion.VERSION)
		job.Setenv("SocketGroup", *flSocketGroup)

		job.SetenvBool("Tls", *flTls)
		job.SetenvBool("TlsVerify", *flTlsVerify)
		job.Setenv("TlsCa", *flCa)
		job.Setenv("TlsCert", *flCert)
		job.Setenv("TlsKey", *flKey)
		if err := job.Run(); err != nil {
			log.Fatal(err)
		}
	} else {
		if flHosts.Len() > 1 {
			log.Fatal("Please specify only one -H")
		}
		protoAddrParts := strings.SplitN(flHosts.GetAll()[0], "://", 2)

		var (
			cli       *api.DockerCli
			tlsConfig tls.Config
		)
		tlsConfig.InsecureSkipVerify = true

		// If we should verify the server, we need to load a trusted ca
		if *flTlsVerify {
			*flTls = true
			certPool := x509.NewCertPool()
			file, err := ioutil.ReadFile(*flCa)
			if err != nil {
				log.Fatalf("Couldn't read ca cert %s: %s", *flCa, err)
			}
			certPool.AppendCertsFromPEM(file)
			tlsConfig.RootCAs = certPool
			tlsConfig.InsecureSkipVerify = false
		}

		// If tls is enabled, try to load and send client certificates
		if *flTls || *flTlsVerify {
			_, errCert := os.Stat(*flCert)
			_, errKey := os.Stat(*flKey)
			if errCert == nil && errKey == nil {
				*flTls = true
				cert, err := tls.LoadX509KeyPair(*flCert, *flKey)
				if err != nil {
					log.Fatalf("Couldn't load X509 key pair: %s. Key encrypted?", err)
				}
				tlsConfig.Certificates = []tls.Certificate{cert}
			}
		}

		if *flTls || *flTlsVerify {
			cli = api.NewDockerCli(os.Stdin, os.Stdout, os.Stderr, protoAddrParts[0], protoAddrParts[1], &tlsConfig)
		} else {
			cli = api.NewDockerCli(os.Stdin, os.Stdout, os.Stderr, protoAddrParts[0], protoAddrParts[1], nil)
		}

		if err := cli.ParseCommands(flag.Args()...); err != nil {
			if sterr, ok := err.(*utils.StatusError); ok {
				if sterr.Status != "" {
					log.Println(sterr.Status)
				}
				os.Exit(sterr.StatusCode)
			}
			log.Fatal(err)
		}
	}
}

func showVersion() {
	fmt.Printf("Docker version %s, build %s\n", dockerversion.VERSION, dockerversion.GITCOMMIT)
}
