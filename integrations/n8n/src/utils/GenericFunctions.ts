import {
	IExecuteFunctions,
	ILoadOptionsFunctions,
	IHttpRequestMethods,
	IHttpRequestOptions,
	IDataObject,
	NodeApiError,
} from 'n8n-workflow';

export async function omnaraApiRequest(
	this: IExecuteFunctions | ILoadOptionsFunctions,
	method: IHttpRequestMethods,
	endpoint: string,
	body: IDataObject = {},
	qs: IDataObject = {},
	uri?: string,
	option: IDataObject = {},
): Promise<any> {
	const credentials = await this.getCredentials('omnaraApi');

	if (!credentials) {
		throw new NodeApiError(this.getNode(), {
			message: 'No credentials found',
		});
	}

	// Debug: Log the credentials to see what we're getting
	console.log('Omnara Credentials:', {
		serverUrl: credentials.serverUrl,
		hasApiKey: !!credentials.apiKey,
	});

	const options: IHttpRequestOptions = {
		method,
		body,
		qs,
		url: uri || `${credentials.serverUrl}/api/v1${endpoint}`,
		json: true,
		...option,
	};

	// Debug logging
	console.log('Omnara API Request:', {
		method,
		url: options.url,
		body: JSON.stringify(body),
		endpoint,
		credentialServerUrl: credentials.serverUrl,
	});

	try {
		const response = await this.helpers.httpRequestWithAuthentication.call(
			this,
			'omnaraApi',
			options,
		);
		console.log('Omnara API Response:', JSON.stringify(response));
		return response;
	} catch (error) {
		console.error('Omnara API Error:', error);
		throw new NodeApiError(this.getNode(), error as any);
	}
}

export function formatMessageResponse(message: any): IDataObject {
	return {
		id: message.id,
		content: message.content,
		senderType: message.sender_type || message.senderType,
		createdAt: message.created_at || message.createdAt,
		requiresUserInput:
			message.requires_user_input !== undefined
				? message.requires_user_input
				: message.requiresUserInput,
	};
}
