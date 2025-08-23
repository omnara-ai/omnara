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

	const options: IHttpRequestOptions = {
		method,
		body,
		qs,
		url: uri || `${credentials.serverUrl}/api/v1${endpoint}`,
		json: true,
		...option,
	};

	try {
		const response = await this.helpers.httpRequestWithAuthentication.call(
			this,
			'omnaraApi',
			options,
		);
		return response;
	} catch (error) {
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
