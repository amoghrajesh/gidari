package transport

import (
	"fmt"
	"net/url"

	"github.com/alpine-hodler/gidari/internal/web"
	"golang.org/x/time/rate"
)

// Request is the information needed to query the web API for data to transport.
type Request struct {
	// Method is the HTTP(s) method used to construct the http request to fetch data for storage.
	Method string `yaml:"method"`

	// Endpoint is the fragment of the URL that will be used to request data from the API. This value can include
	// query parameters.
	Endpoint string `yaml:"endpoint"`

	// Query represent the query params to apply to the URL generated by the request.
	Query map[string]string

	// Timeseries indicates that the underlying data should be queries as a time series. This means that the
	Timeseries *timeseries `yaml:"timeseries"`

	// Table is the name of the table/collection to insert the data fetched from the web API.
	Table *string

	//
	RateLimitConfig *RateLimitConfig `yaml:"rate_limit"`
}

// newFetchConfig will constrcut a new HTTP request from the transport request.
func (req *Request) newFetchConfig(rawURI string, client *web.Client) (*web.FetchConfig, error) {
	rawURIWithEndpoint, err := url.JoinPath(rawURI, req.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to join URI and endpoint: %w", err)
	}

	uri, err := url.Parse(rawURIWithEndpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL: %w", err)
	}

	// Add the query params to the URL.
	if req.Query != nil {
		query := uri.Query()
		for key, value := range req.Query {
			query.Set(key, value)
		}

		uri.RawQuery = query.Encode()
	}

	// create a rate limiter to pass to all "flattenedRequest". This has to be defined outside of the scope of
	// individual "flattenedRequest"s so that they all share the same rate limiter, even concurrent requests to
	// different endpoints could cause a rate limit error on a web API.
	rateLimiter := rate.NewLimiter(rate.Every(*req.RateLimitConfig.Period), *req.RateLimitConfig.Burst)

	return &web.FetchConfig{
		Method:      req.Method,
		URL:         uri,
		C:           client,
		RateLimiter: rateLimiter,
	}, nil
}

// flattenedRequest contains all of the request information to create a web job. The number of flattened request  for an
// operation should be 1-1 with the number of requests to the web API.
type flattenedRequest struct {
	fetchConfig *web.FetchConfig
	table       *string
}

// flatten will compress the request information into a "web.FetchConfig" request and a "table" name for storage
// interaction.
func (req *Request) flatten(rawURI string, client *web.Client) (*flattenedRequest, error) {
	fetchConfig, err := req.newFetchConfig(rawURI, client)
	if err != nil {
		return nil, fmt.Errorf("failed to create fetch config: %w", err)
	}

	return &flattenedRequest{
		fetchConfig: fetchConfig,
		table:       req.Table,
	}, nil
}

// flattenTimeseries will compress the request information into a "web.FetchConfig" request and a "table" name for
// storage interaction. This function will create a flattened request for each time series in the request. If no
// timeseries are defined, this function will return a single flattened request.
func (req *Request) flattenTimeseries(rawURI string, client *web.Client) ([]*flattenedRequest, error) {
	timeseries := req.Timeseries
	if timeseries == nil {
		flatReq, err := req.flatten(rawURI, client)
		if err != nil {
			return nil, fmt.Errorf("failed to flatten request: %w", err)
		}

		return []*flattenedRequest{flatReq}, nil
	}

	requests := make([]*flattenedRequest, 0, len(timeseries.chunks))

	for _, chunk := range timeseries.chunks {
		// copy the request and update it to reflect the partitioned timeseries
		chunkReq := req
		chunkReq.Query[timeseries.StartName] = chunk[0].Format(*timeseries.Layout)
		chunkReq.Query[timeseries.EndName] = chunk[1].Format(*timeseries.Layout)

		fetchConfig, err := chunkReq.newFetchConfig(rawURI, client)
		if err != nil {
			return nil, fmt.Errorf("failed to create fetch config: %w", err)
		}

		requests = append(requests, &flattenedRequest{
			fetchConfig: fetchConfig,
			table:       req.Table,
		})
	}

	return requests, nil
}
