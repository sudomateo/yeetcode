package leetcode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"
)

// Client is the LeetCode API client.
type Client struct {
	httpClient *http.Client
}

// NewClient builds and returns a LeetCode API client ready for use.
func NewClient() Client {
	return Client{
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// Difficulty represents the difficulty of LeetCode questions.
type Difficulty string

// The enumeration of difficulties.
const (
	DifficultyEasy   Difficulty = "EASY"
	DifficultyMedium Difficulty = "MEDIUM"
	DifficultyHard   Difficulty = "HARD"
)

// RandomDifficulty computes a random LeetCode problem difficulty.
func RandomDifficulty() Difficulty {
	switch rand.IntN(3) {
	case 0:
		return DifficultyEasy
	case 1:
		return DifficultyMedium
	case 2:
		return DifficultyHard
	default:
		return DifficultyEasy
	}
}

// The GraphQL query to fetch a random LeetCode question.
const RandomQuestionQuery = `
query randomQuestion($categorySlug: String, $filters: QuestionListFilterInput) {
    randomQuestion(categorySlug: $categorySlug, filters: $filters) {
        titleSlug
    }
}`

// RandomQuestionRequest is the request that's sent to the randomQuestion
// GraphQL API.
type RandomQuestionRequest struct {
	Query     string                  `json:"query"`
	Variables RandomQuestionVariables `json:"variables"`
}

// RandomQuestionVariables are variables that can be set on the request to the
// randomQuestion GraphQL API.
type RandomQuestionVariables struct {
	CategorySlug string                `json:"categorySlug"`
	Filters      RandomQuestionFilters `json:"filters"`
}

// RandomQuestionFilters are filters that can be set on the request to the
// randomQuestion GraphQL API.
type RandomQuestionFilters struct {
	Difficulty Difficulty `json:"difficulty"`
	Tags       []string   `json:"tags,omitempty"`
}

// RandomQuestionResponse is the response sent back from the randomQuestion
// GraphQL API.
type RandomQuestionResponse struct {
	Data struct {
		RandomQuestion struct {
			TitleSlug string `json:"titleSlug"`
		} `json:"randomQuestion"`
	} `json:"data"`
}

// RandomQuestion retrieves a random LeetCode problem.
func (c Client) RandomQuestion(difficulty Difficulty) (RandomQuestionResponse, error) {
	requestBody := RandomQuestionRequest{
		Query: RandomQuestionQuery,
		Variables: RandomQuestionVariables{
			Filters: RandomQuestionFilters{
				Difficulty: difficulty,
			},
		},
	}

	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(requestBody); err != nil {
		return RandomQuestionResponse{}, fmt.Errorf("failed encoding request body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, "https://leetcode.com/graphql", &body)
	if err != nil {
		return RandomQuestionResponse{}, fmt.Errorf("failed building http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://leetcode.com")
	req.Header.Set("Referer", "https://leetcode.com")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return RandomQuestionResponse{}, fmt.Errorf("failed making http request: %w", err)
	}
	defer resp.Body.Close()

	var lcResp RandomQuestionResponse
	if err := json.NewDecoder(resp.Body).Decode(&lcResp); err != nil {
		return RandomQuestionResponse{}, fmt.Errorf("failed decoding http response: %w", err)
	}

	return lcResp, nil
}
