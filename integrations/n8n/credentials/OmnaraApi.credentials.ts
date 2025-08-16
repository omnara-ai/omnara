import {
	IAuthenticateGeneric,
	ICredentialTestRequest,
	ICredentialType,
	INodeProperties,
} from 'n8n-workflow';

export class OmnaraApi implements ICredentialType {
	name = 'omnaraApi';
	displayName = 'Omnara API';
	documentationUrl = 'https://github.com/omnara/omnara';
	properties: INodeProperties[] = [
		{
			displayName: 'API Key',
			name: 'apiKey',
			type: 'string',
			typeOptions: {
				password: true,
			},
			default: '',
			required: true,
			description: 'Your Omnara API key for authentication',
		},
		{
			displayName: 'API URL',
			name: 'apiUrl',
			type: 'string',
			default: 'https://agent-dashboard-mcp.onrender.com',
			required: true,
			description: 'The base URL for the Omnara API',
			placeholder: 'https://agent-dashboard-mcp.onrender.com',
		},
	];

	authenticate: IAuthenticateGeneric = {
		type: 'generic',
		properties: {
			headers: {
				Authorization: '=Bearer {{$credentials.apiKey}}',
			},
		},
	};

	test: ICredentialTestRequest = {
		request: {
			baseURL: '={{$credentials.apiUrl}}',
			url: '/api/v1/messages/pending',
			method: 'GET',
			qs: {
				agent_instance_id: 'test-connection',
			},
		},
		rules: [
			{
				type: 'responseCode',
				properties: {
					value: 200,
					message: 'Connection successful!',
				},
			},
			{
				type: 'responseCode',
				properties: {
					value: 401,
					message: 'Invalid API key. Please check your credentials.',
				},
			},
			{
				type: 'responseCode',
				properties: {
					value: 400,
					message: 'Connection works but test endpoint returned an error (this is expected for test).',
				},
			},
		],
	};
}