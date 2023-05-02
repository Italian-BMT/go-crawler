package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
)

type extractedInfo struct {
	lineNum   string
	stationNm string
	naverCode int
}

var INFO = map[string][]map[string]interface{}{}
var wg = new(sync.WaitGroup)

func run(num int) {
	var baseURL string = "https://pts.map.naver.com/end-subway/ends/web/"

	// 200개 돌며 네이버 코드 스크래핑 (feat. 고루틴)
	c := make(chan extractedInfo)
	for i := (num - 1) * 200; i < (num * 200); i++ {
		wg.Add(1)
		go scrapeNavercode(i, baseURL, c)
		wg.Done()
	}
	wg.Wait()

	// go 루틴에서 채널로 값 받아오기 & 받은 값을 이후에 처리하기 쉬운 형태로 가공
	for i := (num - 1) * 200; i < (num * 200); i++ {
		result := <-c
		lineNum := result.lineNum
		block := make(map[string]interface{})

		// 만약 받아온 값이 없다면 무시하고 아니라면 값 정리
		if result.stationNm == "" || result.lineNum == "" {
			continue
		} else {
			block["stationNm"] = result.stationNm
			block["naverCode"] = result.naverCode
		}

		// 만약 key에 호선이 없으면 새로운 key로 추가 후 정보 입력
		_, ok := INFO[lineNum]
		if ok == false {
			INFO[lineNum] = []map[string]interface{}{}
		}
		INFO[lineNum] = append(INFO[lineNum], block)

		// naverCode 기준으로 오름차순 정렬
		sort.Slice(INFO[lineNum], func(i, j int) bool {
			return INFO[lineNum][i]["naverCode"].(int) < INFO[lineNum][j]["naverCode"].(int)
		})
	}

}

func scrapeNavercode(code int, baseURL string, c chan<- extractedInfo) {
	pageURL := baseURL + strconv.Itoa(code) + "/home"

	// pageURL로 접속하기
	res, err := http.Get(pageURL)
	checkErr(err)
	checkCode(res)

	// 작업 끝나면 res.Body 닫아주는 명령 예약
	defer res.Body.Close()

	// html 읽기
	doc, err := goquery.NewDocumentFromReader(res.Body)
	checkErr(err)

	// 지하철 정보 파싱 후 채널로 보내기
	lineNum := doc.Find(".line_no").Text()
	stationNm := doc.Find(".place_name").Text()
	fmt.Println(code, " 확인완료")

	c <- extractedInfo{
		lineNum:   lineNum,
		stationNm: stationNm,
		naverCode: code}
}

// map을 json 형태로 변환 후 파일 쓰기
func writeFile(fileName string, INFO map[string][]map[string]interface{}) {
	content, _ := json.MarshalIndent(INFO, "", " ")
	_ = os.WriteFile(fileName, content, 0644)
}

// 에러 체킹용 함수 1
func checkErr(err error) {
	if err != nil {
		log.Fatalln(err)
	}
}

// 에러 체킹용 함수 2
func checkCode(res *http.Response) {
	if res.StatusCode != 200 {
		log.Fatalln("Request failed with Status:", res.StatusCode)
	}
}

func HandleRequest(ctx context.Context) (string, error) {
	start := time.Now()

	// 네이버 서버에 부담을 덜기 위해 batch로 나눠서 크롤링 진행 (batch 마다 5초 휴식)
	// tcp: broken pipe 에러를 피하기 위해 최대한 작게 배치 사이즈 설정
	for batchNum := 1; batchNum < 101; batchNum++ {
		fmt.Println(batchNum, "번째 배치 돌기 시작")
		// run 함수 내에서 wg.Wait()를 통해 동시 너무 많이 접속 시도를 하지 않도록 제어
		run(batchNum)
		fmt.Println(batchNum, "번째 배치 돌고, 5초 쉬기 시작")
		time.Sleep(time.Second * 5)
	}

	// 크롤링 결과 파일로 저장하기
	fileName := "subway_information.json"
	lambdaFileName := "/tmp/" + fileName
	writeFile(lambdaFileName, INFO)
	end := time.Since(start)
	fmt.Println("총 실행 시간 : ", end)

	// 저장한 json 파일 s3에 업로드
	sess := session.Must(session.NewSession())
	uploader := s3manager.NewUploader(sess)
	f, err := os.Open(lambdaFileName)
	// file close 하는거 예약하기
	defer f.Close()
	if err != nil {
		return "failed to open file", fmt.Errorf("%q, %v", fileName, err)
	}

	_, err = uploader.Upload(&s3manager.UploadInput{
		Bucket: aws.String("bucketestmy"),
		Key:    aws.String("bmt/" + fileName),
		Body:   f,
	})
	if err != nil {
		return "failed to upload file", fmt.Errorf("%v", err)
	}

	message := fileName + " file uploaded"
	return message, nil
}

func main() {
	lambda.Start(HandleRequest)
}
