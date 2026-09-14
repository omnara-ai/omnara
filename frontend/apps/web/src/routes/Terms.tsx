import { Link } from '@tanstack/react-router'
import type { ReactNode } from 'react'

import { BrandMark } from '@/components/brand/OmnaraMark'

const LAST_UPDATED = 'September 14, 2026'
const PRIVACY_POLICY_URL = 'https://www.omnara.com/privacy'

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <h2 className="type-card-title text-foreground">{title}</h2>
      {children}
    </section>
  )
}

function Paragraph({ children }: { children: ReactNode }) {
  return <p className="text-muted-foreground text-sm leading-relaxed">{children}</p>
}

function List({ items }: { items: ReactNode[] }) {
  return (
    <ul className="text-muted-foreground flex list-disc flex-col gap-1.5 pl-5 text-sm leading-relaxed">
      {items.map((item, index) => (
        <li key={index}>{item}</li>
      ))}
    </ul>
  )
}

function Notice({ children }: { children: ReactNode }) {
  return (
    <div className="bg-muted/40 text-foreground rounded-lg border p-4 text-sm font-medium leading-relaxed">
      {children}
    </div>
  )
}

const externalLinkClass = 'text-foreground font-medium underline-offset-4 hover:underline'

export function Terms() {
  return (
    <div className="bg-background text-foreground min-h-svh">
      <main className="mx-auto flex w-full max-w-3xl flex-col gap-10 px-6 py-10 sm:px-10 sm:py-14">
        <header className="flex flex-col gap-6">
          <Link to="/" className="type-card-title flex items-center gap-2 text-base">
            <BrandMark className="size-5" />
            Omnara
          </Link>
          <div className="flex flex-col gap-2">
            <h1 className="type-title">Terms of Service</h1>
            <p className="text-muted-foreground text-sm">Last updated: {LAST_UPDATED}</p>
          </div>
        </header>

        <div className="flex flex-col gap-8">
          <Section title="1. Acceptance of Terms">
            <Paragraph>
              These Terms are an agreement between you and Omnara, Inc. (&quot;Omnara,&quot;
              &quot;we,&quot; &quot;our,&quot; or &quot;us&quot;). By creating an account, or by
              accessing or using Omnara Cloud at app.omnara.com, the Omnara API, the Omnara CLI and
              SDKs, or any other hosted service we provide (collectively, the &quot;Service&quot;),
              you agree to be bound by these Terms. If you do not agree to these Terms, do not use
              the Service.
            </Paragraph>
            <Paragraph>
              If you use the Service on behalf of a company or other organization, you represent
              that you have authority to bind that organization to these Terms, and &quot;you&quot;
              refers to that organization.
            </Paragraph>
            <Paragraph>
              The Omnara source code is available separately under the Apache License 2.0. These
              Terms govern your use of the hosted Service, not your use of the open-source software
              in a self-hosted deployment, which is governed by that license.
            </Paragraph>
          </Section>

          <Section title="2. Description of Service">
            <Paragraph>
              Omnara provides a hosted platform for building, running, and managing AI agents. The
              Service includes:
            </Paragraph>
            <List
              items={[
                'The Omnara web console at app.omnara.com',
                'The Omnara REST API, CLI, and SDKs',
                'Durable agent execution and storage of agent state and event history',
                'Omnara-managed model providers and Omnara-managed sandboxes, which consume credits',
                'The ability to connect your own model providers, sandbox providers, and machines',
                'Encrypted storage of secrets and credentials you provide',
                'Tools, skills, MCP server connections, and first-party integrations such as Slack',
                'Organizations, projects, roles, and permissions for controlling access to the above',
              ]}
            />
          </Section>

          <Section title="3. Accounts and Organizations">
            <Paragraph>To use the Service, you must:</Paragraph>
            <List
              items={[
                'Provide accurate and complete registration information and keep it up to date',
                'Maintain the security of your account credentials, API tokens, and machine tokens',
                'Be responsible for all activity under your account, including activity by API tokens and machines you register',
                'Notify us immediately at contact@omnara.com of any unauthorized use of your account',
              ]}
            />
            <Paragraph>
              Organization owners and administrators are responsible for the members they invite,
              the roles and grants they assign, and the activity of users and API keys within their
              organization.
            </Paragraph>
          </Section>

          <Section title="4. Payment Terms">
            <Paragraph>
              Certain features of the Service, including Omnara-managed model providers and
              Omnara-managed sandboxes, consume credits. You agree to the following:
            </Paragraph>
            <List
              items={[
                'Credits and other paid features are purchased through our billing provider (currently Stripe).',
                'Credits are consumed based on your usage, including usage generated by agents you run and by users you give access to.',
                'Except where required by law, purchased credits are non-refundable.',
                'We may change our pricing, the features included in each plan, or the rate at which credits are consumed. Changes will not apply retroactively to credits already consumed.',
                'Usage may be subject to operational limits. Agents may pause or fail if your organization has insufficient credits.',
                'When you bring your own model provider, sandbox provider, or machines, you are billed directly by those providers under their terms, not by Omnara.',
              ]}
            />
          </Section>

          <Section title="5. Acceptable Use">
            <Paragraph>
              You may use the Service for any lawful purpose. You agree NOT to use the Service, or
              agents you run on the Service, to:
            </Paragraph>
            <List
              items={[
                'Violate any applicable law or regulation, or infringe the rights of others',
                'Access, probe, or attack systems, networks, or accounts that you are not authorized to access',
                'Transmit malware or harmful code, or interfere with or disrupt the Service',
                'Circumvent access controls, tool permissions, approvals, rate limits, or usage limits, or attempt to gain unauthorized access to other users\u2019 organizations or data',
                'Impersonate others or provide false information',
                'Harass, harm, or defraud other users or third parties',
                'Violate the usage policies of the model providers, sandbox providers, or other third-party services you use through the Service',
                'Resell or redistribute the Service without our written permission, other than by building products that use the Service as described in these Terms',
              ]}
            />
          </Section>

          <Section title="6. Your Content and Your Agents">
            <List
              items={[
                'You retain ownership of the content you provide to or create through the Service, including agent configurations, instructions, inputs, outputs, artifacts, and event history (\u201cYour Content\u201d).',
                'You grant us a license to host, store, process, transmit, and display Your Content as necessary to provide, secure, and improve the Service and to comply with law.',
                'You are responsible for Your Content and for the agents you configure and run, including the instructions you give them, the tools and permissions you grant them, and the actions they take on machines, sandboxes, and third-party systems.',
                'If you build products for your own users on the Service, you are responsible for those users, for obtaining any consents required from them, and for their compliance with these Terms.',
                'Tool policies and approvals are controls you configure. You are responsible for choosing settings appropriate to the risk of the actions your agents can take.',
                'Omnara and its licensors own the Service, including its software, design, and trademarks. Nothing in these Terms transfers those rights to you.',
              ]}
            />
          </Section>

          <Section title="7. Privacy">
            <Paragraph>
              Your use of the Service is subject to our{' '}
              <a
                href={PRIVACY_POLICY_URL}
                target="_blank"
                rel="noreferrer"
                className={externalLinkClass}
              >
                Privacy Policy
              </a>
              , which is incorporated into these Terms by reference.
            </Paragraph>
          </Section>

          <Section title="8. Third-Party Services">
            <List
              items={[
                'The Service integrates with third-party services, including model providers, sandbox providers, MCP servers, and messaging platforms such as Slack.',
                'Your use of these services is subject to their own terms and policies, whether you access them through Omnara-managed providers or by bringing your own accounts.',
                'We are not responsible for the availability, output, or conduct of third-party services.',
                'API keys, tokens, and other credentials you provide are your responsibility. We store them encrypted and use them only to provide the Service as you configure it, but you remain responsible for their scope and rotation.',
              ]}
            />
          </Section>

          <Section title="9. Your Machines">
            <Paragraph>
              You may connect your own laptops, servers, or other machines to the Service. If you
              do:
            </Paragraph>
            <List
              items={[
                'You are responsible for the security, configuration, and maintenance of those machines and for any data on them',
                'You are responsible for the actions agents take on those machines, including commands run and files changed',
                'You represent that you are authorized to connect each machine and to grant agents the access you configure',
              ]}
            />
          </Section>

          <Section title="10. Disclaimers">
            <Notice>
              THE SERVICE IS PROVIDED &quot;AS IS&quot; AND &quot;AS AVAILABLE&quot; WITHOUT
              WARRANTIES OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING MERCHANTABILITY, FITNESS FOR A
              PARTICULAR PURPOSE, NON-INFRINGEMENT, OR ACCURACY OF INFORMATION.
            </Notice>
            <Paragraph>WE DO NOT WARRANT THAT:</Paragraph>
            <List
              items={[
                'The Service will be uninterrupted or error-free',
                'AI-generated output will be accurate, complete, appropriate, or suitable for your purposes',
                'Agents will take only the actions you intend; AI agents are probabilistic and may misinterpret instructions',
              ]}
            />
          </Section>

          <Section title="11. Limitation of Liability">
            <Paragraph>TO THE MAXIMUM EXTENT PERMITTED BY LAW:</Paragraph>
            <List
              items={[
                'We are not liable for indirect, incidental, special, punitive, or consequential damages, or for lost profits, revenue, or data',
                'Our total liability for all claims arising out of or relating to the Service shall not exceed the amount you paid us for the Service in the twelve months before the claim',
                <>
                  We are not responsible for losses resulting from:
                  <ul className="mt-1.5 flex list-[circle] flex-col gap-1 pl-5">
                    <li>Your use of or inability to use the Service</li>
                    <li>Unauthorized access to your account, tokens, or data</li>
                    <li>
                      Data breaches, hacks, or security incidents beyond our reasonable control
                    </li>
                    <li>Loss, corruption, or disclosure of your data</li>
                    <li>AI-generated content, recommendations, or actions taken by agents</li>
                    <li>Actions taken on your machines or third-party systems by agents you run</li>
                    <li>Third-party services or content</li>
                  </ul>
                </>,
              ]}
            />
          </Section>

          <Section title="12. Indemnification">
            <Paragraph>
              You agree to indemnify and hold harmless Omnara, its affiliates, and their respective
              officers, directors, employees, and agents from any claims, damages, losses,
              liabilities, costs, and expenses arising from your violation of these Terms, your use
              of the Service, Your Content, the agents you run and the actions they take, your
              users, or your violation of any rights of another party.
            </Paragraph>
          </Section>

          <Section title="13. Termination">
            <List
              items={[
                'Either party may terminate these Terms at any time. You may stop using the Service and request deletion of your account by contacting contact@omnara.com.',
                'We may suspend or terminate your access for violations of these Terms, for non-payment, or to protect the Service or other users.',
                'Upon termination, your right to use the Service ends. Running agents may be stopped and access to the API revoked.',
                'You can export Your Content through the API before termination. After termination we may delete Your Content in accordance with our Privacy Policy and data retention practices.',
                'Provisions that by their nature should survive termination, including Sections 6, 10, 11, 12, and 15, will remain in effect.',
              ]}
            />
          </Section>

          <Section title="14. Modifications">
            <Paragraph>
              We may modify these Terms at any time. If we make material changes, we will post the
              updated Terms and update the &quot;Last updated&quot; date. Continued use after
              changes take effect constitutes acceptance. We may also change, suspend, or
              discontinue features of the Service, including features offered in beta or early
              access, at any time.
            </Paragraph>
          </Section>

          <Section title="15. Governing Law">
            <Paragraph>
              These Terms are governed by the laws of the State of Delaware, without regard to
              conflict of law principles. Any dispute arising out of or relating to these Terms or
              the Service will be resolved exclusively in the state or federal courts located in
              Delaware, and you consent to the personal jurisdiction of those courts.
            </Paragraph>
          </Section>

          <Section title="16. Contact Information">
            <Paragraph>For questions about these Terms, contact us at:</Paragraph>
            <div className="text-muted-foreground flex flex-col gap-1 text-sm">
              <p>
                Email:{' '}
                <a href="mailto:contact@omnara.com" className={externalLinkClass}>
                  contact@omnara.com
                </a>
              </p>
              <p>
                Website:{' '}
                <a
                  href="https://www.omnara.com"
                  target="_blank"
                  rel="noreferrer"
                  className={externalLinkClass}
                >
                  https://www.omnara.com
                </a>
              </p>
            </div>
          </Section>
        </div>

        <footer className="text-muted-foreground flex items-center justify-center gap-3 border-t pt-6 text-xs">
          <Link to="/" className="hover:text-foreground">
            Home
          </Link>
          <span aria-hidden>•</span>
          <a
            href={PRIVACY_POLICY_URL}
            target="_blank"
            rel="noreferrer"
            className="hover:text-foreground"
          >
            Privacy Policy
          </a>
        </footer>
      </main>
    </div>
  )
}
