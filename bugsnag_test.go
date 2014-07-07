package bugsnag

import (
	"fmt"
	"github.com/bitly/go-simplejson"
	"github.com/mitchellh/osext"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConfigure(t *testing.T) {
	Configure(Configuration{
		APIKey: testAPIKey,
	})

	if Config.APIKey != testAPIKey {
		t.Errorf("Setting APIKey didn't work")
	}

	if NewNotifier().Config.APIKey != testAPIKey {
		t.Errorf("Setting APIKey didn't work for new notifiers")
	}
}

var postedJson = make(chan []byte, 10)
var testOnce sync.Once
var testEndpoint string
var testAPIKey = "166f5ad3590596f9aa8d601ea89af845"

func startTestServer() {
	testOnce.Do(func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			body, err := ioutil.ReadAll(r.Body)
			if err != nil {
				panic(err)
			}
			postedJson <- body
		})

		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			panic(err)
		}
		testEndpoint = "http://" + l.Addr().String() + "/"

		go http.Serve(l, mux)
	})
}

type _recurse struct {
	*_recurse
}

func TestNotify(t *testing.T) {
	startTestServer()

	recurse := _recurse{}
	recurse._recurse = &recurse

	OnBeforeNotify(func(event *Event, config *Configuration) bool {
		if event.Context == "testing" {
			event.GroupingHash = "lol"
		}
		return true
	})

	Notify(fmt.Errorf("hello world"),
		Configuration{
			APIKey:          testAPIKey,
			Endpoint:        testEndpoint,
			ReleaseStage:    "test",
			AppVersion:      "1.2.3",
			Hostname:        "web1",
			ProjectPackages: []string{"github.com/bugsnag/bugsnag-go"},
		},
		User{Id: "123", Name: "Conrad", Email: "me@cirw.in"},
		Context{"testing"},
		MetaData{"test": {
			"password": "sneaky",
			"value":    "able",
			"broken":   complex(1, 2),
			"recurse":  recurse,
		}},
	)

	json, err := simplejson.NewJson(<-postedJson)

	if err != nil {
		t.Fatal(err)
	}

	if json.Get("apiKey").MustString() != testAPIKey {
		t.Errorf("Wrong api key in payload")
	}

	if json.GetPath("notifier", "name").MustString() != "Bugsnag Go" {
		t.Errorf("Wrong notifier name in payload")
	}

	event := json.Get("events").GetIndex(0)

	for k, value := range map[string]string{
		"payloadVersion":                 "2",
		"severity":                       "warning",
		"context":                        "testing",
		"groupingHash":                   "lol",
		"app.releaseStage":               "test",
		"app.version":                    "1.2.3",
		"device.hostname":                "web1",
		"user.id":                        "123",
		"user.name":                      "Conrad",
		"user.email":                     "me@cirw.in",
		"metaData.test.password":         "[REDACTED]",
		"metaData.test.value":            "able",
		"metaData.test.broken":           "[complex128]",
		"metaData.test.recurse._recurse": "[RECURSION]",
	} {
		key := strings.Split(k, ".")
		if event.GetPath(key...).MustString() != value {
			t.Errorf("Wrong %v: %v != %v", key, event.GetPath(key...).MustString(), value)
		}
	}

	exception := event.Get("exceptions").GetIndex(0)

	if exception.Get("message").MustString() != "hello world" {
		t.Errorf("Wrong message in payload")
	}

	if exception.Get("errorClass").MustString() != "*errors.errorString" {
		t.Errorf("Wrong errorClass in payload: %v", exception.Get("errorClass").MustString())
	}

	frame0 := exception.Get("stacktrace").GetIndex(0)
	if frame0.Get("file").MustString() != "bugsnag_test.go" ||
		frame0.Get("method").MustString() != "TestNotify" ||
		frame0.Get("inProject").MustBool() != true ||
		frame0.Get("lineNumber").MustInt() == 0 {
		t.Errorf("Wrong frame0")
	}

	frame1 := exception.Get("stacktrace").GetIndex(1)

	if frame1.Get("file").MustString() != "testing/testing.go" ||
		frame1.Get("method").MustString() != "tRunner" ||
		frame1.Get("inProject").MustBool() != false ||
		frame1.Get("lineNumber").MustInt() == 0 {
		t.Errorf("Wrong frame1")
	}
}

func crashyHandler(w http.ResponseWriter, r *http.Request) {
	c := make(chan int)
	close(c)
	c <- 1
}

func runCrashyServer(rawData ...interface{}) (net.Listener, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", crashyHandler)
	srv := http.Server{
		Addr:     l.Addr().String(),
		Handler:  Handler(mux, rawData...),
		ErrorLog: log.New(ioutil.Discard, log.Prefix(), log.Flags()),
	}

	go srv.Serve(l)
	return l, err
}

func TestHandler(t *testing.T) {
	startTestServer()

	l, err := runCrashyServer(Configuration{
		APIKey:          testAPIKey,
		Endpoint:        testEndpoint,
		ProjectPackages: []string{"github.com/bugsnag/bugsnag-go"},
		Logger:          log.New(ioutil.Discard, log.Prefix(), log.Flags()),
	}, SeverityInfo)
	if err != nil {
		t.Fatal(err)
	}
	http.Get("http://" + l.Addr().String() + "/ok?foo=bar")
	l.Close()

	json, err := simplejson.NewJson(<-postedJson)
	if err != nil {
		t.Fatal(err)
	}

	if json.Get("apiKey").MustString() != testAPIKey {
		t.Errorf("Wrong api key in payload")
	}

	if json.GetPath("notifier", "name").MustString() != "Bugsnag Go" {
		t.Errorf("Wrong notifier name in payload")
	}

	event := json.Get("events").GetIndex(0)

	for k, value := range map[string]string{
		"payloadVersion":          "2",
		"severity":                "info",
		"user.id":                 "127.0.0.1",
		"metaData.Request.Url":    "http://" + l.Addr().String() + "/ok?foo=bar",
		"metaData.Request.Method": "GET",
	} {
		key := strings.Split(k, ".")
		if event.GetPath(key...).MustString() != value {
			t.Errorf("Wrong %v: %v != %v", key, event.GetPath(key...).MustString(), value)
		}
	}

	if event.GetPath("metaData", "Request", "Params", "foo").GetIndex(0).MustString() != "bar" {
		t.Errorf("missing GET params in request metadata")
	}

	if event.GetPath("metaData", "Headers", "Accept-Encoding").GetIndex(0).MustString() != "gzip" {
		t.Errorf("missing GET params in request metadata: %v", event.GetPath("metaData", "Headers"))
	}

	exception := event.Get("exceptions").GetIndex(0)

	if exception.Get("message").MustString() != "runtime error: send on closed channel" {
		t.Errorf("Wrong message in payload: %v", exception.Get("message").MustString())
	}

	if exception.Get("errorClass").MustString() != "runtime.errorCString" {
		t.Errorf("Wrong errorClass in payload: %v", exception.Get("errorClass").MustString())
	}

	// TODO:CI these are probably dependent on go version.
	frame0 := exception.Get("stacktrace").GetIndex(0)
	if frame0.Get("file").MustString() != "runtime/panic.c" ||
		frame0.Get("method").MustString() != "panicstring" ||
		frame0.Get("inProject").MustBool() != false ||
		frame0.Get("lineNumber").MustInt() == 0 {
		t.Errorf("Wrong frame0: %v", frame0)
	}

	frame3 := exception.Get("stacktrace").GetIndex(3)

	if frame3.Get("file").MustString() != "bugsnag_test.go" ||
		frame3.Get("method").MustString() != "crashyHandler" ||
		frame3.Get("inProject").MustBool() != true ||
		frame3.Get("lineNumber").MustInt() == 0 {
		t.Errorf("Wrong frame3: %v", frame3)
	}
}

func TestAutoNotify(t *testing.T) {

	var panicked interface{}

	func() {
		defer func() {
			panicked = recover()
		}()
		defer AutoNotify(Configuration{Endpoint: testEndpoint, APIKey: testAPIKey})

		panic("eggs")
	}()

	if panicked.(string) != "eggs" {
		t.Errorf("didn't re-panic")
	}

	json, err := simplejson.NewJson(<-postedJson)
	if err != nil {
		t.Fatal(err)
	}

	event := json.Get("events").GetIndex(0)

	if event.Get("severity").MustString() != "error" {
		t.Errorf("severity should be error")
	}
	exception := event.Get("exceptions").GetIndex(0)

	if exception.Get("message").MustString() != "eggs" {
		t.Errorf("caught wrong panic")
	}
}

func TestRecover(t *testing.T) {
	var panicked interface{}

	func() {
		defer func() {
			panicked = recover()
		}()
		defer Recover(Configuration{Endpoint: testEndpoint, APIKey: testAPIKey})

		panic("ham")
	}()

	if panicked != nil {
		t.Errorf("re-panick'd")
	}

	json, err := simplejson.NewJson(<-postedJson)
	if err != nil {
		t.Fatal(err)
	}

	event := json.Get("events").GetIndex(0)

	if event.Get("severity").MustString() != "warning" {
		t.Errorf("severity should be warning")
	}
	exception := event.Get("exceptions").GetIndex(0)

	if exception.Get("message").MustString() != "ham" {
		t.Errorf("caught wrong panic")
	}
}

func TestPanicHandler(t *testing.T) {
	startTestServer()

	exePath, err := osext.Executable()
	if err != nil {
		t.Fatal(err)
	}

	// Use the same trick as panicwrap() to re-run ourselves.
	// In the init() block below, we will then panic.
	cmd := exec.Command(exePath, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "BUGSNAG_API_KEY="+testAPIKey, "BUGSNAG_ENDPOINT="+testEndpoint)

	for i, _ := range cmd.Env {
		if cmd.Env[i] == "bugsnag_wrapped=bugsnag_wrapped" {
			cmd.Env[i] = "please_panic=please_panic"
		}
	}

	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}

	if err = cmd.Wait(); err.Error() != "exit status 2" {
		t.Fatal(err)
	}

	json, err := simplejson.NewJson(<-postedJson)
	if err != nil {
		t.Fatal(err)
	}

	event := json.Get("events").GetIndex(0)

	if event.Get("severity").MustString() != "error" {
		t.Errorf("severity should be error")
	}
	exception := event.Get("exceptions").GetIndex(0)

	if exception.Get("message").MustString() != "ruh roh" {
		t.Errorf("caught wrong panic")
	}

	if exception.Get("errorClass").MustString() != "panic" {
		t.Errorf("caught wrong panic")
	}

	frame := exception.Get("stacktrace").GetIndex(1)

	// Yeah, we just caught a panic from the init() function below and sent it to the server running above (mindblown)
	if frame.Get("inProject").MustBool() != true ||
		frame.Get("file").MustString() != "bugsnag_test.go" ||
		frame.Get("method").MustString() != "panick" ||
		frame.Get("lineNumber").MustInt() == 0 {
		t.Errorf("stack trace seemed wrong")
	}
}

func init() {
	if os.Getenv("please_panic") != "" {
		Configure(Configuration{APIKey: os.Getenv("BUGSNAG_API_KEY"), Endpoint: os.Getenv("BUGSNAG_ENDPOINT"), ProjectPackages: []string{"github.com/bugsnag/bugsnag-go"}})
		go func() {
			panick()
		}()
		// Plenty of time to crash, it shouldn't need any of it.
		time.Sleep(1 * time.Second)
	}
}

func panick() {
	panic("ruh roh")
}

func handleGet(w http.ResponseWriter, r *http.Request) {

}

var createAccount = handleGet

type _job struct {
	Name    string
	Process func()
}

func ExampleAutoNotify() interface{} {
	return func(w http.ResponseWriter, request *http.Request) {
		defer AutoNotify(request, Context{"createAccount"})

		createAccount(w, request)
	}
}

func ExampleRecover(job _job) {
	go func() {
		defer Recover(Context{job.Name}, SeverityWarning)

		job.Process()
	}()
}

func ExampleConfigure() {
	Configure(Configuration{
		APIKey: "YOUR_API_KEY_HERE",

		ReleaseStage: "production",

		// See Configuration{} for other fields
	})
}

func ExampleHandler() {
	// Set up your http handlers as usual
	http.HandleFunc("/", handleGet)

	// use bugsnag.Handler(nil) to wrap the default http handlers
	// so that Bugsnag is automatically notified about panics.
	http.ListenAndServe(":1234", Handler(nil))
}

func ExampleHandler_customServer() {
	// If you're using a custom server, set the handlers explicitly.
	http.HandleFunc("/", handleGet)

	srv := http.Server{
		Addr:        ":1234",
		ReadTimeout: 10 * time.Second,
		// use bugsnag.Handler(nil) to wrap the default http handlers
		// so that Bugsnag is automatically notified about panics.
		Handler: Handler(nil),
	}
	srv.ListenAndServe()
}

func ExampleHandler_customHandlers() {
	// If you're using custom handlers, wrap the handlers explicitly.
	handler := http.NewServeMux()
	http.HandleFunc("/", handleGet)
	// use bugsnag.Handler(handler) to wrap the handlers so that Bugsnag is
	// automatically notified about panics
	http.ListenAndServe(":1234", Handler(handler))
}

func ExampleNotify() {
	_, err := net.Listen("tcp", ":80")

	if err != nil {
		Notify(err)
	}
}

func ExampleNotify_details(userId string) {
	_, err := net.Listen("tcp", ":80")

	if err != nil {
		Notify(err,
			// show as low-severity
			SeverityInfo,
			// set the context
			Context{"createlistener"},
			// pass the user id in to count users affected.
			User{Id: userId},
			// custom meta-data tab
			MetaData{
				"Listen": {
					"Protocol": "tcp",
					"Port":     "80",
				},
			},
		)
	}

}

type Job struct {
	Retry     bool
	UserId    string
	UserEmail string
	Name      string
	Params    map[string]string
}

func ExampleOnBeforeNotify() {
	OnBeforeNotify(func(event *Event, config *Configuration) bool {

		// Search all the RawData for any *Job pointers that we're passed in
		// to bugsnag.Notify() and friends.
		for _, datum := range event.RawData {
			if job, ok := datum.(*Job); ok {
				// don't notify bugsnag about errors in retries
				if job.Retry {
					return false
				}

				// add the job as a tab on Bugsnag.com
				event.MetaData.AddStruct("Job", job)

				// set the user correctly
				event.User = &User{Id: job.UserId, Email: job.UserEmail}
			}
		}

		// continue notifying as normal
		return true
	})
}

func ExampleOnAroundNotify() {
	OnAroundNotify(func(event *Event, config *Configuration, next func()) {
		start := time.Now()
		next()
		config.Logger.Printf("bugsnag.notify took: %v", time.Now().Sub(start))
	})
}