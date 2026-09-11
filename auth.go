package tgtg

import (
	"context"
	"errors"
)

type authTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func (tok authTokens) validate() error {
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		return errors.New("authentication response requires nonempty access_token and refresh_token")
	}
	return nil
}

func (c *Client) storeTokens(tok authTokens, resp httpResponse) {
	c.commitAuthCookies(resp)
	c.AccessToken = tok.AccessToken
	c.RefreshToken = tok.RefreshToken
	c.LastTimeTokenRefreshed = c.now()
}

// Authentication cookie updates remain provisional until the response's
// required fields are validated. DataDome acquisition still uses the live jar.
func (c *Client) postAuth(ctx context.Context, requestURL string, body any) (httpResponse, error) {
	client := *c.httpClient
	cookies := &cookieTransaction{base: client.Jar.(*ownedCookieJar)}
	client.Jar = cookies
	resp, err := c.postWithClient(ctx, requestURL, body, &client)
	if err != nil {
		return httpResponse{}, err
	}
	resp.cookies = cookies
	return resp, nil
}

func (c *Client) commitAuthCookies(resp httpResponse) {
	if resp.cookies != nil {
		resp.cookies.commit()
	}
	c.syncCookies()
}
