import type {
	IAuthenticateGeneric,
	ICredentialTestRequest,
	ICredentialType,
	INodeProperties,
} from 'n8n-workflow';

export class OmnaraApi implements ICredentialType {
	name = 'omnaraApi';
	displayName = 'Omnara API';
	documentationUrl = 'https://github.com/omnara-ai/omnara';
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
			description: 'Your Omnara API key (JWT token)',
		},
		{
			displayName: 'Base URL',
			name: 'baseUrl',
			type: 'string',
			default: 'https://agent-dashboard-mcp.onrender.com',
			description: 'The base URL of your Omnara instance',
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
			baseURL: '={{$credentials.baseUrl}}',
			url: '/api/v1/messages/pending?agent_instance_id=test',
			method: 'GET',
		},
		rules: [
			{
				type: 'responseCode',
				properties: {
					value: 400,
					message: 'Authentication successful (test endpoint returned expected error)',
				},
			},
		],
	};
}