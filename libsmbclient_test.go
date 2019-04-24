package libsmbclient

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"text/template"
	_ "time"
)

var SMB_CONF_TEMPLATE = `[global]
workgroup = TESTGROUP
interfaces = lo 127.0.0.0/8
smb ports = 1445
log level = 2
map to guest = Bad User
passdb backend = smbpasswd
smb passwd file = {{.Tempdir}}/smbpasswd
lock directory = {{.Tempdir}}/intern
state directory = {{.Tempdir}}/intern
cache directory = {{.Tempdir}}/intern
pid directory = {{.Tempdir}}/intern
private dir = {{.Tempdir}}/intern
ncalrpc dir = {{.Tempdir}}/intern

[public]
path = {{.Tempdir}}/public
guest ok = yes

[private]
path = {{.Tempdir}}/private
read only = no
`

// global teardown funcitons
var teardown []func()

func generateSmbdConf() string {
	tempdir, _ := filepath.Abs("./tmp/samba")
	teardown = append(teardown, func() {
		os.RemoveAll(tempdir)
	})
	paths := [...]string{
		tempdir,
		filepath.Join(tempdir, "samaba", "private"),
		filepath.Join(tempdir, "samba", "public"),
	}
	for _, d := range paths {
		err := os.MkdirAll(d, 0755)
		if err != nil {
			log.Fatal(err)
		}
	}
	os.Mkdir(filepath.Join(tempdir, "private"), 0755)
	os.Mkdir(filepath.Join(tempdir, "public"), 0755)
	f, err := os.Create(filepath.Join(tempdir, "smbd.conf"))
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	templateText := SMB_CONF_TEMPLATE
	type Dir struct {
		Tempdir string
	}
	t, err := template.New("smb-conf").Parse(templateText)
	if err != nil {
		log.Fatal(err)
	}
	t.Execute(f, Dir{tempdir})
	return f.Name()
}

func startSmbd() {
	// thanks pitti :)
	os.Setenv("LIBSMB_PROG", "nc localhost 1445")
	smb_conf := generateSmbdConf()
	cmd := exec.Command("smbd", "-FS", "-s", smb_conf)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	teardown = append(teardown, func() {
		cmd.Process.Kill()
		cmd.Process.Wait()
	})
}

func setUp() {
	startSmbd()
}

func tearDown() {
	// cleanup
	for _, f := range teardown {
		f()
	}
	// cleanup atexit
	teardown = nil
}

func TestLibsmbclientBindings(t *testing.T) {
	fmt.Println("libsmbclient opendir test")

	setUp()

	// open client
	client := New()
	d, err := client.Opendir("smb://localhost")
	if err != nil {
		t.Error("failed to open localhost ", err)
	}

	// collect dirs
	foundSmbDirs := map[string]bool{}
	for {
		dirent, err := d.Readdir()
		if err != nil {
			break
		}
		foundSmbDirs[dirent.Name] = true
	}
	// check for expected data
	if !foundSmbDirs["private"] || !foundSmbDirs["public"] {
		t.Error(fmt.Sprintf("missing excpected folder in (%v)", foundSmbDirs))
	}

	tearDown()
}

func getRandomFileName() string {
	return fmt.Sprintf("%d", rand.Int())
}

func openFile(client *Client, path string, c chan int) {
	f, err := client.Open(path, 0, 0)
	if err != nil {
		log.Fatal(fmt.Sprintf("%s: %s", path, err))
	}
	defer func() {
		f.Close()
		c <- 1
	}()

	// FIXME: move this into the lib as ioutil.ReadFile()
	buf := make([]byte, 64*1024)
	for {
		_, err := f.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Print(err)
			return
		}
	}
}

func readAllFilesInDir(client *Client, baseDir string, c chan int) {
	d, err := client.Opendir(baseDir)
	if err != nil {
		log.Fatal(fmt.Sprintf("%s: %s", baseDir, err))
	}
	defer d.Closedir()
	for {
		dirent, err := d.Readdir()
		if err != nil {
			break
		}
		if dirent.Name == "." || dirent.Name == ".." {
			continue
		}
		if dirent.Type == SMBC_DIR {
			go readAllFilesInDir(client, baseDir+dirent.Name+"/", c)
		}
		if dirent.Type == SMBC_FILE {
			go openFile(client, baseDir+dirent.Name, c)
		}
	}
}

func TestLibsmbclientThreaded(t *testing.T) {
	fmt.Println("libsmbclient threaded test")

	setUp()

	CLIENTS := 4
	DIRS := 4
	THREADS := 8
	FILE_SIZE := 4 * 1024

	for i := 0; i < DIRS; i++ {
		dirname := fmt.Sprintf("./tmp/samba/public/%d/", i)
		os.MkdirAll(dirname, 0755)

		// create a bunch of test files
		buf := make([]byte, FILE_SIZE)
		for j := 0; j < THREADS; j++ {
			tmpf := dirname + getRandomFileName()
			ioutil.WriteFile(tmpf, buf, 0644)
		}

	}

	// make N clients
	c := make(chan int)
	for i := 0; i < CLIENTS; i++ {
		baseDir := "smb://localhost/public/"
		client := New()
		// FIXME: close eventually
		//defer client.Close()
		go readAllFilesInDir(client, baseDir, c)
	}

	count := 0
	for count < THREADS*DIRS*CLIENTS {
		count += <-c
	}

	fmt.Println(fmt.Sprintf("done: %d", count))
	tearDown()
}
