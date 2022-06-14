package provider

import (
	"context"
	"fmt"
	"io/ioutil"
	"mime"
	"net/http"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type dataSourceHTTPType struct {
}

func (d dataSourceHTTPType) GetSchema(_ context.Context) (tfsdk.Schema, diag.Diagnostics) {
	return tfsdk.Schema{
		Description: `
The ` + "`http`" + ` data source makes an HTTP GET request to the given URL and exports
information about the response.

The given URL may be either an ` + "`http`" + ` or ` + "`https`" + ` URL. At present this resource
can only retrieve data from URLs that respond with ` + "`text/*`" + ` or
` + "`application/json`" + ` content types, and expects the result to be UTF-8 encoded
regardless of the returned content type header.

~> **Important** Although ` + "`https`" + ` URLs can be used, there is currently no
mechanism to authenticate the remote server except for general verification of
the server certificate's chain of trust. Data retrieved from servers not under
your control should be treated as untrustworthy.`,

		Attributes: map[string]tfsdk.Attribute{
			"url": {
				Description: "The URL for the request. Supported schemes are `http` and `https`.",
				Type:        types.StringType,
				Required:    true,
			},

			"request_headers": {
				Description: "A map of request header field names and values.",
				Type: types.MapType{
					ElemType: types.StringType,
				},
				Optional: true,
			},

			"body": {
				Description: "The response body returned as a string. " +
					"**NOTE**: This is deprecated, use `response_body` instead.",
				Type:               types.StringType,
				Computed:           true,
				DeprecationMessage: "**NOTE**: This is deprecated, use `response_body` instead.",
			},

			"response_body": {
				Description: "The response body returned as a string.",
				Type:        types.StringType,
				Computed:    true,
			},

			"response_headers": {
				Description: `A map of response header field names and values.` +
					` Duplicate headers are concatenated with according to [RFC2616](https://www.w3.org/Protocols/rfc2616/rfc2616-sec4.html#sec4.2).`,
				Type: types.MapType{
					ElemType: types.StringType,
				},
				Computed: true,
			},

			"id": {
				Description: "The ID of this resource.",
				Type:        types.StringType,
				Computed:    true,
			},
		},
	}, nil
}

func (d dataSourceHTTPType) NewDataSource(ctx context.Context, p tfsdk.Provider) (tfsdk.DataSource, diag.Diagnostics) {
	return dataSourceHTTP{
		p: *(p.(*provider)),
	}, nil
}

type dataSourceHTTP struct {
	p provider
}

type HTTPModel struct {
	ID              types.String `tfsdk:"id"`
	URL             types.String `tfsdk:"url"`
	RequestHeaders  types.Map    `tfsdk:"request_headers"`
	ResponseHeaders types.Map    `tfsdk:"response_headers"`
	Body            types.String `tfsdk:"body"`
	ResponseBody    types.String `tfsdk:"response_body"`
}

func (d dataSourceHTTP) Read(ctx context.Context, req tfsdk.ReadDataSourceRequest, resp *tfsdk.ReadDataSourceResponse) {
	var model HTTPModel
	diags := req.Config.Get(ctx, &model)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	url := model.URL.Value
	headers := model.RequestHeaders

	client := &http.Client{}

	request, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating request",
			fmt.Sprintf("Error creating request: %s", err),
		)
		return
	}

	for name, value := range headers.Elems {
		var header string
		diags = tfsdk.ValueAs(ctx, value, &header)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}

		request.Header.Set(name, header)
	}

	response, err := client.Do(request)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error making request",
			fmt.Sprintf("Error making request: %s", err),
		)
		return
	}

	defer response.Body.Close()

	if response.StatusCode != 200 {
		resp.Diagnostics.AddError(
			"HTTP response code error",
			fmt.Sprintf("Response code is not 200: %d", response.StatusCode),
		)
		return
	}

	contentType := response.Header.Get("Content-Type")
	if !isContentTypeText(contentType) {
		resp.Diagnostics.AddWarning(
			fmt.Sprintf("Content-Type is not recognized as a text type, got %q", contentType),
			"If the content is binary data, Terraform may not properly handle the contents of the response.",
		)
	}

	bytes, err := ioutil.ReadAll(response.Body)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading response body",
			fmt.Sprintf("Error reading response body: %s", err),
		)
		return
	}

	responseBody := string(bytes)

	responseHeaders := make(map[string]string)
	for k, v := range response.Header {
		// Concatenate according to RFC2616
		// cf. https://www.w3.org/Protocols/rfc2616/rfc2616-sec4.html#sec4.2
		responseHeaders[k] = strings.Join(v, ", ")
	}

	respHeadersState := types.Map{}

	diags = tfsdk.ValueFrom(ctx, responseHeaders, types.Map{ElemType: types.StringType}.Type(ctx), &respHeadersState)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	model.ID = types.String{Value: url}
	model.ResponseHeaders = respHeadersState
	model.Body = types.String{Value: responseBody}
	model.ResponseBody = types.String{Value: responseBody}

	diags = resp.State.Set(ctx, model)
	resp.Diagnostics.Append(diags...)
}

// This is to prevent potential issues w/ binary files
// and generally unprintable characters
// See https://github.com/hashicorp/terraform/pull/3858#issuecomment-156856738
func isContentTypeText(contentType string) bool {

	parsedType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}

	allowedContentTypes := []*regexp.Regexp{
		regexp.MustCompile("^text/.+"),
		regexp.MustCompile("^application/json$"),
		regexp.MustCompile(`^application/samlmetadata\+xml`),
	}

	for _, r := range allowedContentTypes {
		if r.MatchString(parsedType) {
			charset := strings.ToLower(params["charset"])
			return charset == "" || charset == "utf-8" || charset == "us-ascii"
		}
	}

	return false
}
