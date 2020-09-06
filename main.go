package main

import (
	"compress/flate"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/service/s3"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"

	"github.com/fatih/color"
	"github.com/jasonlvhit/gocron"
	"github.com/jpillora/backoff"
	"github.com/manifoldco/promptui"
	"github.com/mholt/archiver"
	"github.com/urfave/cli"
)

var (
	Format          string = "format"
	AwsBucket       string = "aws-bucket"
	AwsRegion       string = "aws-region"
	DgraphHost      string = "dgraph-host"
	FilePrefix      string = "file-prefix"
	AwsKey          string = "aws-key"
	AwsSecret       string = "aws-secret"
	ExportPath      string = "export-path"
	CronEveryMinute string = "cron-every-minute"
	HostName        string = "hostname"
	DgraphAlphaHost string = "dgraph-alpha-host"
	DgraphAlphaPort string = "dgraph-alpha-port"
	DgraphZeroHost  string = "dgraph-zero-host"
	DgraphZeroPort  string = "dgraph-zero-port"
)

func requestExport(c *cli.Context) (success bool) {
	var requestUri = c.String(DgraphHost) + "/admin/export?format=" + c.String(Format)
	yellow := color.New(color.FgYellow).SprintFunc()
	fmt.Printf("Requesting Export from %s \n", yellow(requestUri))
	req, err := http.NewRequest("GET", requestUri, nil)
	if err != nil {
		fmt.Println(err)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Panic("Cannot access to dgraph server", err)
	}
	defer resp.Body.Close()
	if http.StatusOK == resp.StatusCode {
		bodyBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Fatal(err)
		}
		bodyString := string(bodyBytes)
		isSuccess := strings.Contains(bodyString, "Success")
		if isSuccess != true {
			log.Fatal("Export Failed", bodyString)
			return false
		}
		color.Blue("Data export successful")
		return true
	}
	return false
}

func zipIt(c *cli.Context) (filePath string, err error) {
	z := archiver.Zip{
		CompressionLevel: flate.DefaultCompression,
	}
	fileName := "./" + c.String(FilePrefix) + "-" + c.String(HostName) + "-" + time.Now().Format(time.RFC3339) + ".zip"
	err = z.Archive([]string{c.String(ExportPath)}, fileName)
	if err != nil {
		log.Fatal("err Zipping", err)
		return "", err
	}
	return fileName, nil
}

func shipIt(c *cli.Context, filename string) error {
	// The session the S3 Uploader will use
	sess := session.Must(session.NewSession(&aws.Config{
		Region:      aws.String(c.String(AwsRegion)),
		Credentials: credentials.NewStaticCredentials(c.String(AwsKey), c.String(AwsSecret), ""),
	}))

	// Create an uploader with the session and default options
	uploader := s3manager.NewUploader(sess)

	f, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("failed to open file %q, %v", filename, err)
	}

	// Upload the file to S3.
	result, err := uploader.Upload(&s3manager.UploadInput{
		Bucket: aws.String(c.String(AwsBucket)),
		Key:    aws.String(filename),
		Body:   f,
	})
	if err != nil {
		return fmt.Errorf("failed to upload file, %v", err)
	}
	info := color.New(color.FgWhite, color.BgGreen).SprintFunc()
	fmt.Printf("%s Uploaded to %s \n", info("[DONE]"), aws.StringValue(&result.Location))
	return nil
}

func cleanUp(c *cli.Context, filePath string) {
	err := os.Remove(filePath)
	if err != nil {
		fmt.Println("remove error.", err)
	}
	err = os.RemoveAll(c.String(ExportPath))
	if err != nil {
		fmt.Println("remove all error.", err)
	}
}

func Export(c *cli.Context) {
	success := requestExport(c)
	if success {
		b := &backoff.Backoff{
			Max: 5 * time.Minute,
		}
		for {
			_, err := os.Stat(c.String(ExportPath))
			if os.IsNotExist(err) {
				d := b.Duration()
				log.Printf("Export is not ready yet, retrying in  %s", d)
				time.Sleep(d)
				if b.Attempt() == 1 {
					log.Println("NO SUCCESS  after 10 try")
					os.Exit(0)
				}
				continue
			}
			b.Reset()
			filePath, _ := zipIt(c)
			err = shipIt(c, filePath)
			if err != nil {
				color.Red("Failed to upload", err)
			}
			cleanUp(c, filePath)
			color.Green("SUCCESS")
			break
		}
	}
}

func cronjob(c *cli.Context) {
	gocron.Every(c.Uint64(CronEveryMinute)).Minutes().Do(Export, c)
	<-gocron.Start()
}

func getBackUpFile(sess *session.Session, c *cli.Context) (key string) {

	// Create S3 service client
	svc := s3.New(sess)
	resp, err := svc.ListObjectsV2(&s3.ListObjectsV2Input{Bucket: aws.String(c.String(AwsBucket))})
	if err != nil {
		log.Println("ERROR getting bucket")
	}

	// sorting by key (newest will appear first)
	// sort.Slice(resp.Contents, func(i, j int) bool {
	// 	return *resp.Contents[i].Key > *resp.Contents[j].Key
	// })

	templates := &promptui.SelectTemplates{
		Label:    "{{ . }}?",
		Active:   "\U0001F336 {{ .Key }} ({{ .Size }})",
		Inactive: "  {{ .Key }} ({{ .Size }})",
		Selected: "\U0001F336 {{ .Key }}",
		Details: `
--------- BACKUP FILE ----------
{{ "Name:" | faint }}	{{ .Key }}
{{ "Size:" | faint }}	{{ .Size }}
{{ "Last Modified:" | faint }}	{{ .LastModified }}`,
	}

	prompt := promptui.Select{
		Label:     "Select BackupTo Restore",
		Items:     resp.Contents,
		Templates: templates,
	}
	_, result, err := prompt.Run()
	r := regexp.MustCompile(`Key[:]\s\"(.*)\"`)
	key = r.FindStringSubmatch(result)[1]
	return key
}

func DownloadFile(c *cli.Context, sess *session.Session, fileName string) (filepath string) {
	downloader := s3manager.NewDownloader(sess)
	file, err := os.Create(fileName)
	if err != nil {
		log.Fatal("Unable to open file %q, %v", err)
	}
	defer file.Close()
	numBytes, err := downloader.Download(file,
		&s3.GetObjectInput{
			Bucket: aws.String(c.String(AwsBucket)),
			Key:    aws.String(fileName),
		})
	if err != nil {
		log.Fatal("Unable to download item %q, %v", fileName, err)
	}
	fmt.Println("Downloaded", file.Name(), numBytes, "bytes")
	err = archiver.Unarchive("./"+file.Name(), "data")
	return "./data/exports/"
}

func getFiles(locPath string) (rdfFiles []string, schemaFiles []string, err error) {
	err = filepath.Walk(locPath, func(path string, info os.FileInfo, err error) error {
		if strings.Contains(path, "gz") {
			filePathPieces := strings.Split(path, ".")
			filePrefix := len(filePathPieces) - 2
			if filePathPieces[filePrefix] == "rdf" {
				rdfFiles = append(rdfFiles, path)
			} else if filePathPieces[filePrefix] == "schema" {
				schemaFiles = append(schemaFiles, path)
			}
		}
		return nil
	})
	if err != nil {
		log.Panic("No file Found", err)
		return nil, nil, err
	}
	return rdfFiles, schemaFiles, nil
}

func Restore(c *cli.Context) {
	sess := session.Must(session.NewSession(&aws.Config{
		Region:      aws.String(c.String(AwsRegion)),
		Credentials: credentials.NewStaticCredentials(c.String(AwsKey), c.String(AwsSecret), ""),
	}))
	file := getBackUpFile(sess, c)
	filePath := DownloadFile(c, sess, file)
	alphaAddr := c.String(DgraphAlphaHost) + ":" + c.String(DgraphAlphaPort)
	zeroAddr := c.String(DgraphZeroHost) + ":" + c.String(DgraphZeroPort)
	log.Println(filePath, alphaAddr, zeroAddr)
	_, schemaFiles, _ := getFiles(filePath)
	log.Println(schemaFiles)
	log.Println("COMMAND", "dgraph", "live", "-f", filePath, "-a", alphaAddr, "-z", zeroAddr, "-s", schemaFiles[0])
	cmd := exec.Command("dgraph", "live", "-f", filePath, "-a", alphaAddr, "-z", zeroAddr, "-s", schemaFiles[0])
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fmt.Println(err) // replace with logger, or anything you want
	}
	defer stdin.Close() // the doc says subProcess.Wait will close it, but I'm not sure, so I kept this line

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	fmt.Println("START")               // for debug
	if err = cmd.Start(); err != nil { // Use start, not run
		fmt.Println("An error occured: ", err) // replace with logger, or anything you want
	}
	io.WriteString(stdin, "4\n")
	cmd.Wait()
	fmt.Println("END") // for debug
}

func main() {

	ifaces, err := net.Interfaces()
	// handle err
	for _, i := range ifaces {
		addrs, _ := i.Addrs()
		// handle err
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			// process IP address
			fmt.Println(ip)
		}
	}

	app := cli.NewApp()
	app.Name = "dgraph-backup"
	Flags := []cli.Flag{
		cli.StringFlag{
			Name:   "format",
			Value:  "json",
			EnvVar: "EXPORT_FORMAT",
			Usage:  "you can set rdf or json",
		},
		cli.StringFlag{
			Name:   AwsBucket,
			Value:  "dgraph-backup",
			EnvVar: "AWS_BUCKET",
			Usage:  "AWS Bucket",
		},
		cli.StringFlag{
			Name:   AwsRegion,
			Value:  "ap-northeast-2",
			EnvVar: "AWS_REGION",
		},
		cli.StringFlag{
			Name:   DgraphHost,
			Value:  "http://localhost:8080",
			EnvVar: "DGRAPH_HOST",
			Usage:  "Exp: http://localhost:8080",
		},
		cli.StringFlag{
			Name:   FilePrefix,
			Value:  "dgraph-backup",
			EnvVar: "FILE_PREFIX",
			Usage:  "Backup file prefix <prefix>-<timestamp>.zip",
		},
		cli.StringFlag{
			Name:     AwsKey,
			EnvVar:   "AWS_ACCESS_KEY",
			Required: true,
		},
		cli.StringFlag{
			Name:     AwsSecret,
			EnvVar:   "AWS_ACCESS_SECRET",
			Required: true,
		},
		cli.StringFlag{
			Name:   ExportPath,
			EnvVar: "EXPORT_PATH",
			Value:  "./export",
		},
		cli.Uint64Flag{
			Name:   CronEveryMinute,
			EnvVar: "CRON_EVERY_MINUTE",
			Value:  1,
		},
		cli.StringFlag{
			Name:   HostName,
			EnvVar: "HOSTNAME",
		},
		cli.StringFlag{
			Name:   DgraphAlphaHost,
			EnvVar: "DGRAPH_ALPHA_PUBLIC_GRPC_PORT_9080_TCP_ADDR",
			Value:  "localhost",
		},
		cli.StringFlag{
			Name:   DgraphAlphaPort,
			EnvVar: "DGRAPH_ALPHA_PUBLIC_GRPC_SERVICE_PORT",
			Value:  "9080",
		},
		cli.StringFlag{
			Name:   DgraphZeroHost,
			EnvVar: "DGRAPH_ZERO_PUBLIC_GRPC_PORT_5080_TCP_ADDR",
			Value:  "localhost",
		},
		cli.StringFlag{
			Name:   DgraphZeroPort,
			EnvVar: "DGRAPH_ZERO_PUBLIC_GRPC_PORT_5080_TCP_PORT",
			Value:  "5080",
		},
	}
	app.Commands = []cli.Command{
		{
			Name:   "backup-now",
			Action: Export,
			Flags:  Flags,
		},
		{
			Name:   "backup-cron",
			Action: cronjob,
			Flags:  Flags,
		},
		{
			Name:   "restore",
			Action: Restore,
			Flags:  Flags,
		},
	}

	err = app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}
