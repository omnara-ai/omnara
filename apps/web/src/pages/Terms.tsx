import { Link } from "react-router-dom";

const Terms = () => {
  return (
    <div className="min-h-screen bg-charcoal text-white">
      <main className="container mx-auto px-6 py-12 max-w-4xl">
        <div className="mb-8">
          <Link to="/" className="text-primary hover:text-primary/80 transition-colors text-sm mb-4 inline-block">
            ← Back to Home
          </Link>
          <h1 className="text-4xl font-bold mb-4">Terms of Use & End User License Agreement</h1>
          <p className="text-off-white/70">Last Updated: {new Date().toLocaleDateString()}</p>
        </div>

        <div className="prose prose-invert max-w-none space-y-8">
          <section className="bg-yellow-900/20 border border-yellow-600/30 rounded-lg p-4">
            <p className="text-yellow-200 text-sm font-semibold">
              IMPORTANT: PLEASE READ THESE TERMS OF USE ("TERMS") CAREFULLY BEFORE USING THE OMNARA APPLICATION.
            </p>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">1. Acceptance of Terms</h2>
            <p className="text-off-white/90 leading-relaxed">
              By downloading, installing, or using the Omnara mobile application ("App") and related services (collectively, the "Service"), you agree to be bound by these Terms. If you do not agree to these Terms, do not use the Service.
            </p>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">2. Description of Service</h2>
            <p className="text-off-white/90 mb-3">Omnara provides a platform that enables real-time communication between users and their AI agents through various AI service providers. The Service includes:</p>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>Mobile application for iOS and Android</li>
              <li>Web application</li>
              <li>Real-time messaging with AI agents</li>
              <li>Agent status monitoring and management</li>
              <li>Subscription-based premium features</li>
            </ul>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">3. Account Registration</h2>
            <p className="text-off-white/90 mb-3">To use the Service, you must:</p>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>Provide accurate and complete registration information</li>
              <li>Maintain the security of your account credentials</li>
              <li>Be responsible for all activities under your account</li>
              <li>Notify us immediately of any unauthorized use</li>
            </ul>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">4. Subscription and Payment Terms</h2>
            
            <h3 className="text-xl font-semibold mb-3">Free Tier</h3>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>Limited to 10 agent instances per month</li>
              <li>Basic features as described in the App</li>
            </ul>

            <h3 className="text-xl font-semibold mb-3 mt-4">Pro Subscription ($9/month)</h3>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>Unlimited agent instances</li>
              <li>Priority support</li>
              <li>Advanced features as they become available</li>
              <li>Billed monthly through your app store account or Stripe</li>
            </ul>

            <h3 className="text-xl font-semibold mb-3 mt-4">Billing</h3>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>Subscriptions automatically renew unless canceled 24 hours before the current period ends</li>
              <li>Your account will be charged for renewal within 24 hours before the current period ends</li>
              <li>You can manage subscriptions in your app store account settings</li>
              <li>No refunds for partial months</li>
            </ul>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">5. Use of Service</h2>
            <p className="text-off-white/90 mb-3">You may use our Service for any lawful purpose. You agree NOT to:</p>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>Use the Service for illegal purposes</li>
              <li>Transmit malware or harmful code</li>
              <li>Interfere with or disrupt the Service</li>
              <li>Attempt unauthorized access to other users' accounts</li>
              <li>Impersonate others or provide false information</li>
              <li>Harass or harm other users</li>
            </ul>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">6. Content and Ownership</h2>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>You retain ownership of content you create through the Service</li>
              <li>You grant us a license to store and display your content as necessary to provide the Service</li>
            </ul>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">7. Privacy</h2>
            <p className="text-off-white/90 leading-relaxed">
              Your use of the Service is subject to our <Link to="/privacy" className="text-omnara-gold hover:text-omnara-gold-light transition-colors">Privacy Policy</Link>, which is incorporated into these Terms by reference.
            </p>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">8. Third-Party Services</h2>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>The Service integrates with third-party AI providers</li>
              <li>Your use of these integrations is subject to their terms and policies</li>
              <li>We are not responsible for third-party services or content</li>
              <li>API keys and credentials you provide are your responsibility</li>
            </ul>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">9. Disclaimers</h2>
            <div className="bg-red-900/20 border border-red-600/30 rounded-lg p-4 mb-4">
              <p className="text-red-200 text-sm">
                THE SERVICE IS PROVIDED "AS IS" AND "AS AVAILABLE" WITHOUT WARRANTIES OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE, NON-INFRINGEMENT, OR ACCURACY OF INFORMATION.
              </p>
            </div>
            <p className="text-off-white/90 mb-3">WE DO NOT WARRANT THAT:</p>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>The Service will be uninterrupted or error-free</li>
              <li>Defects will be corrected</li>
              <li>The Service is free of viruses or harmful components</li>
              <li>AI responses will be accurate, appropriate, or suitable for your purposes</li>
            </ul>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">10. Limitation of Liability</h2>
            <p className="text-off-white/90 mb-3">TO THE MAXIMUM EXTENT PERMITTED BY LAW:</p>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>We are not liable for indirect, incidental, special, or consequential damages</li>
              <li>Our total liability shall not exceed the amount you paid for the Service in the past 12 months</li>
              <li>We are not responsible for losses resulting from:
                <ul className="list-circle pl-6 mt-2 space-y-1">
                  <li>Your use or inability to use the Service</li>
                  <li>Unauthorized access to your account or data</li>
                  <li>Data breaches, hacks, or security incidents</li>
                  <li>Loss, corruption, or disclosure of your data</li>
                  <li>AI-generated content or recommendations</li>
                  <li>Third-party services or content</li>
                </ul>
              </li>
            </ul>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">11. Indemnification</h2>
            <p className="text-off-white/90 leading-relaxed">
              You agree to indemnify and hold harmless Omnara, its affiliates, and their respective officers, directors, employees, and agents from any claims, damages, losses, liabilities, costs, and expenses arising from your violation of these Terms, your use of the Service, your content or AI interactions, or your violation of any rights of another party.
            </p>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">12. Termination</h2>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>Either party may terminate these Terms at any time</li>
              <li>We may suspend or terminate your access for violations</li>
              <li>Upon termination, your license to use the Service ends</li>
              <li>Provisions that should survive termination will remain in effect</li>
            </ul>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">13. Modifications</h2>
            <p className="text-off-white/90 leading-relaxed">
              We may modify these Terms at any time. We will notify you of material changes through the App or email. Continued use after changes constitutes acceptance.
            </p>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">14. Governing Law</h2>
            <p className="text-off-white/90 leading-relaxed">
              These Terms are governed by the laws of the United States, without regard to conflict of law principles.
            </p>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">15. Apple App Store Additional Terms</h2>
            <p className="text-off-white/90 mb-3">For iOS users:</p>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>Apple has no obligation to provide maintenance or support for the App</li>
              <li>Apple is not responsible for any product warranties</li>
              <li>Apple is not responsible for addressing claims relating to the App</li>
              <li>Apple is not responsible for third-party intellectual property claims</li>
              <li>Apple and its subsidiaries are third-party beneficiaries of these Terms</li>
            </ul>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">16. Google Play Store Additional Terms</h2>
            <p className="text-off-white/90 mb-3">For Android users:</p>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>You acknowledge that these Terms are between you and Omnara, not Google</li>
              <li>Google has no responsibility for the App or its content</li>
              <li>Your use must comply with Google Play's Terms of Service</li>
            </ul>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">17. Contact Information</h2>
            <p className="text-off-white/90 leading-relaxed">
              For questions about these Terms, contact us at:
            </p>
            <div className="mt-3 text-off-white/90">
              <p>Email: contact@omnara.com</p>
              <p>Website: https://claude.omnara.com</p>
            </div>
          </section>

          <section className="bg-blue-900/20 border border-blue-600/30 rounded-lg p-4">
            <p className="text-blue-200 text-sm font-semibold">
              BY USING THE SERVICE, YOU ACKNOWLEDGE THAT YOU HAVE READ, UNDERSTOOD, AND AGREE TO BE BOUND BY THESE TERMS OF USE.
            </p>
          </section>
        </div>
        
        {/* Legal Links Footer */}
        <div className="mt-12 pt-8 border-t border-white/10 text-center">
          <div className="flex justify-center items-center space-x-4 text-sm">
            <Link to="/" className="text-off-white/60 hover:text-omnara-gold transition-colors">
              Home
            </Link>
            <span className="text-off-white/40">•</span>
            <Link to="/privacy" className="text-off-white/60 hover:text-omnara-gold transition-colors">
              Privacy Policy
            </Link>
          </div>
        </div>
      </main>
    </div>
  );
};

export default Terms;