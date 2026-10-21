package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func loadEnvMap(path string) map[string]string {
	env := make(map[string]string)
	f, err := os.Open(path)
	if err != nil {
		return env
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
			env[k] = v
		}
	}
	return env
}

func first(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func main() {
	fmt.Println("==================================================")
	fmt.Println("🧪 Backblaze B2 / S3 Connection & Upload Test")
	fmt.Println("==================================================")

	env := loadEnvMap(".env")
	keyID := first(env["B2_APPLICATION_KEY_ID"], env["B2_KEY_ID"], env["S3_ACCESS_KEY"], env["WASABI_ACCESS_KEY"], os.Getenv("B2_APPLICATION_KEY_ID"))
	appKey := first(env["B2_APPLICATION_KEY"], env["S3_SECRET_KEY"], env["WASABI_SECRET_KEY"], os.Getenv("B2_APPLICATION_KEY"))
	region := first(env["B2_REGION"], env["S3_REGION"], env["WASABI_REGION"], "eu-central-003")
	endpoint := first(env["B2_ENDPOINT"], env["S3_ENDPOINT"], env["WASABI_ENDPOINT"], "https://s3.eu-central-003.backblazeb2.com")
	bucket := first(env["B2_BUCKETS"], env["B2_BUCKET"], env["S3_BUCKETS"], "webt-temp")

	if strings.Contains(bucket, ",") {
		bucket = strings.TrimSpace(strings.Split(bucket, ",")[0])
	}

	fmt.Printf("Bucket:   %s\n", bucket)
	fmt.Printf("Region:   %s\n", region)
	fmt.Printf("Endpoint: %s\n", endpoint)

	if keyID == "" || appKey == "" {
		fmt.Println("\n⚠️ Application Key ID or Application Key is missing in .env!")
		fmt.Println("Please set B2_APPLICATION_KEY_ID and B2_APPLICATION_KEY in .env, then rerun this test:")
		fmt.Println("go run cmd/test_storage/main.go")
		return
	}

	maskedKey := keyID
	if len(maskedKey) > 6 {
		maskedKey = maskedKey[:6] + "..."
	}
	fmt.Printf("Key ID:   %s (length: %d)\n", maskedKey, len(keyID))

	ctx := context.TODO()

	s3Client := s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(keyID, appKey, ""),
		UsePathStyle: true,
	})

	testKey := fmt.Sprintf("test-cloud-tor-%d.txt", time.Now().Unix())
	testData := []byte("Hello from Cloud-Tor! Backblaze B2 verification test succeeded at " + time.Now().Format(time.RFC3339))

	fmt.Printf("\n1. Uploading test file '%s' to B2 bucket '%s'...\n", testKey, bucket)
	_, err := s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(testKey),
		Body:        bytes.NewReader(testData),
		ContentType: aws.String("text/plain"),
	})
	if err != nil {
		fmt.Printf("   ❌ Upload failed: %v\n", err)
		return
	}
	fmt.Println("   ✅ Upload succeeded!")

	// Generate 6-hour Presigned URL
	fmt.Println("2. Generating 6-hour Presigned GET URL on Backblaze B2...")
	presignClient := s3.NewPresignClient(s3Client)
	presignedReq, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(testKey),
	}, s3.WithPresignExpires(6*time.Hour))
	if err != nil {
		fmt.Printf("   ❌ Presign failed: %v\n", err)
		return
	}
	fmt.Printf("   ✅ Presigned URL generated successfully!\n")
	fmt.Printf("   URL: %s\n", presignedReq.URL)

	// Test downloading from the Presigned URL directly over HTTP
	fmt.Println("3. Testing direct HTTP download via the Presigned URL...")
	resp, err := http.Get(presignedReq.URL)
	if err != nil {
		fmt.Printf("   ❌ HTTP GET failed: %v\n", err)
		return
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("   ❌ HTTP status %d: %s\n", resp.StatusCode, string(bodyBytes))
		return
	}
	fmt.Printf("   ✅ HTTP GET returned 200 OK! Content: '%s'\n", string(bodyBytes))

	// Clean up: delete test object
	fmt.Println("4. Cleaning up test object from Backblaze B2 bucket...")
	_, err = s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(testKey),
	})
	if err != nil {
		fmt.Printf("   ⚠️ Note: Failed to delete test object: %v\n", err)
	} else {
		fmt.Println("   ✅ Test object cleaned up successfully from Backblaze B2!")
	}

	fmt.Println("\n🎉 SUCCESS! Backblaze B2 is 100% working with Cloud-Tor!")
}
