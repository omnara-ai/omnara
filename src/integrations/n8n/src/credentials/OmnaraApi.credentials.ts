import {
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
			description: 'Your Omnara API key. Get it from your Omnara dashboard.',
		},
		{
			displayName: 'API URL',
			name: 'serverUrl',
			type: 'string',
			default: 'https://agent.omnara.com',
			required: true,
			description:
				'The Omnara API URL (without /api/v1). Use the default unless you have a custom deployment.',
			placeholder: 'https://agent.omnara.com',
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
			baseURL: '={{$credentials.serverUrl}}',
			url: '/api/v1/auth/verify',
			method: 'GET',
		},
	};
}
