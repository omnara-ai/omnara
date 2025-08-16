import {
	IExecuteFunctions,
	IHookFunctions,
	IHttpRequestMethods,
	IRequestOptions,
	IDataObject,
	NodeApiError,
	NodeOperationError,
	JsonObject,
} from 'n8n-workflow';

export async function omnaraApiRequest(
	this: IHookFunctions | IExecuteFunctions,
	method: IHttpRequestMethods,
	endpoint: string,
	body: IDataObject = {},
	qs: IDataObject = {},
	uri?: string,
	option: IDataObject = {},
): Promise<any> {
	const credentials = await this.getCredentials('omnaraApi');
	
	if (!credentials) {
		throw new NodeOperationError(this.getNode(), 'No credentials provided for Omnara API');
	}

	const apiUrl = credentials.apiUrl as string || 'https://agent-dashboard-mcp.onrender.com';
	
	const options: IRequestOptions = {
		method,
		headers: {
			'Accept': 'application/json',
			'Content-Type': 'application/json',
			'Authorization': `Bearer ${credentials.apiKey}`,
		},
		body: method !== 'GET' ? body : undefined,
		qs,
		uri: uri || `${apiUrl}${endpoint}`,
		json: true,
		...option,
	};

	try {
		const response = await this.helpers.request(options);
		return response;
	} catch (error) {
		// Type guard for error handling
		const err = error as any;
		
		// Handle specific error cases
		if (err.statusCode === 401) {
			throw new NodeApiError(this.getNode(), err as JsonObject, {
				message: 'Invalid API key or authentication failed',
			});
		}
		
		if (err.statusCode === 400) {
			throw new NodeApiError(this.getNode(), err as JsonObject, {
				message: err.message || 'Bad request to Omnara API',
			});
		}

		if (err.statusCode === 404) {
			throw new NodeApiError(this.getNode(), err as JsonObject, {
				message: 'Resource not found',
			});
		}

		if (err.statusCode === 429) {
			throw new NodeApiError(this.getNode(), err as JsonObject, {
				message: 'Rate limit exceeded. Please try again later.',
			});
		}

		if (err.statusCode >= 500) {
			throw new NodeApiError(this.getNode(), err as JsonObject, {
				message: 'Omnara server error. Please try again later.',
			});
		}

		throw new NodeApiError(this.getNode(), err as JsonObject);
	}
}

export function validateUUID(uuid: string): boolean {
	const uuidRegex = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
	return uuidRegex.test(uuid);
}