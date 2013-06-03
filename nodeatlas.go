package main

import (
	"database/sql"
	"flag"
	"fmt"
	"github.com/inhies/go-utils/log"
	"html/template"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"syscall"
	"time"
)

var Version = "0.1"

const (
	DefaultLogLevel = log.INFO
)

var (
	Conf *Config
	t    *template.Template
	l    *log.Logger
)

var (
	fConf = flag.String("conf", "conf.json", "path to configuration file")
	fRes  = flag.String("res", "res/", "path to resource directory")

	fDebug    = flag.Bool("debug", false, "maximize verbosity")
	fReadOnly = flag.Bool("readonly", false, "disallow database changes")

	fTestDB = flag.Bool("testdb", false, "test the database")
)

func main() {
	flag.Parse()

	// Load the configuration.
	var err error
	Conf, err = ReadConfig(*fConf)
	if err != nil {
		fmt.Printf("Could not read conf: %s", err)
		os.Exit(1)
	}

	// Initialize the log with an appropriate log level. Do this
	// inside a separate scope so that variables can be garbage
	// collected.
	{
		var level log.LogLevel
		flags := log.Ldate | log.Ltime // Logging flags
		if *fDebug {
			level = log.DEBUG
			flags |= log.Lshortfile // Include the filename and line
		} else {
			level = DefaultLogLevel
		}
		l, err = log.NewLevel(level, true, os.Stdout, "", flags)
		if err != nil {
			fmt.Printf("Could start logger: %s", err)
			os.Exit(1)
		}
	}

	// Listen for OS signals.
	go ListenSignal()

	l.Infof("Starting NodeAtlas %s\n", Version)

	if *fTestDB {
		// Open up a temporary database using sqlite3.
		tempDB := path.Join(os.TempDir(), "nodeatlas-test.db")

		db, err := sql.Open("sqlite3", tempDB)
		if err != nil {
			l.Fatalf("Could not connect to temporary database: %s", err)
		}

		// Perform all of the tests in sequence.
		TestDatabase(DB{db, false})
		err = db.Close()
		if err != nil {
			l.Emergf("Could not close temporary database: %s", err)
		}

		// Finally, remove the database file and exit.
		err = os.Remove(tempDB)
		if err != nil {
			l.Emergf("Could not remove temporary database %q: %s",
				tempDB, err)
		}
		return
	}

	// Connect to the database with configured parameters.
	db, err := sql.Open(Conf.Database.DriverName,
		Conf.Database.Resource)
	if err != nil {
		l.Fatalf("Could not connect to database: %s", err)
	}
	// Wrap the *sql.DB type.
	Db = DB{
		DB:       db,
		ReadOnly: (*fReadOnly || Conf.Database.ReadOnly),
	}
	l.Debug("Connected to database\n")
	if Db.ReadOnly {
		l.Debug("Database is read only\n")
	}

	// Initialize the database with all of its tables.
	err = Db.InitializeTables()
	if err != nil {
		l.Fatalf("Could not initialize database: %s", err)
	}
	l.Debug("Initialized database\n")
	l.Infof("Nodes: %d (%d local)\n", Db.LenNodes(true), Db.LenNodes(false))

	// Start the HTTP server.
	err = StartServer(Conf.Addr, Conf.Prefix)
	if err != nil {
		l.Fatalf("Server crashed: %s", err)
	}
}

// StartServer is a simple helper function to register any handlers
// (such as the API) and start the HTTP server on the given
// address. If it crashes, it returns the error.
func StartServer(addr, prefix string) (err error) {
	// Register any handlers.
	RegisterAPI(prefix)
	l.Debug("Registered API handler\n")

	err = RegisterTemplates()
	if err != nil {
		return
	}
	http.HandleFunc("/", HandleRoot)
	http.HandleFunc("/res/", HandleRes)
	http.HandleFunc("/favicon.ico", HandleIcon)

	// Start the HTTP server and return any errors if it crashes.
	l.Infof("Starting HTTP server on %q\n", addr)
	return http.ListenAndServe(addr, nil)
}

// RegisterTemplates loads templates from <*fRes>/webpages/*.html and
// <*fRes>/emails/*.txt into the global variable t.
func RegisterTemplates() (err error) {
	t, err = template.ParseGlob(path.Join(*fRes, "webpages/*html"))
	if err != nil {
		return
	}
	_, err = t.ParseGlob(path.Join(*fRes, "emails/*.txt"))
	return
}

// ListenSignal uses os/signal to wait for OS signals, such as SIGHUP
// and SIGINT, and perform the appropriate actions as listed below.
//     SIGHUP: reload configuration file
func ListenSignal() {
	// Create the channel and use signal.Notify to listen for any
	// specified signals.
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)
	for sig := range c {
		switch sig {
		case syscall.SIGHUP:
			l.Info("Caught SIGHUP; reloading config\n")
			conf, err := ReadConfig(*fConf)
			if err != nil {
				l.Errf("Could not read conf; using old one: %s", err)
				continue
			}
			Conf = conf
		}
	}
}

func TestDatabase(db DB) {
	err := db.InitializeTables()
	if err != nil {
		l.Fatalf("Could not initialize tables: %s", err)
	}
	l.Debug("Successfully initialized tables\n")

	node := &Node{
		Addr:       IP(net.ParseIP("ff00::1")),
		OwnerName:  "nodeatlas",
		OwnerEmail: "admin@example.org",
		Latitude:   80.01010,
		Longitude:  -80.10101,
		Status:     StatusPossible,
	}

	nodeCached := &Node{
		Addr:         IP(net.ParseIP("ff00::2")),
		OwnerName:    "test",
		OwnerEmail:   "nothing@example.com",
		Latitude:     34.14523,
		Longitude:    5.3635,
		Status:       StatusPossible,
		RetrieveTime: time.Now().Unix(),
	}

	err = db.AddNode(node)

	if err != nil {
		l.Errf("Error adding node: %s", err)
	} else {
		l.Debug("Successfully added node\n")
	}

	l.Debugf("Nodes: %d", db.LenNodes(false))

	node.Status = StatusActive
	err = db.UpdateNode(node)
	if err != nil {
		l.Errf("Error updating node: %s", err)
	} else {
		l.Debug("Successfully updated node")
	}

	ip := IP(net.ParseIP("ff00::1"))
	_, err = db.GetNode(ip)
	if err != nil {
		l.Errf("Error retrieving node: %s", err)
	} else {
		l.Debug("Successfully got node")
	}

	err = db.DeleteNode(node.Addr)
	if err != nil {
		l.Errf("Error deleting node: %s", err)
	} else {
		l.Debug("Successfully deleted node")
	}

	nodes := []*Node{node, nodeCached}

	err = db.CacheNodes(nodes, "example.com")
	if err != nil {
		l.Errf("Error caching nodes: %s", err)
	} else {
		l.Debug("Successfully cached nodes")
	}
}
