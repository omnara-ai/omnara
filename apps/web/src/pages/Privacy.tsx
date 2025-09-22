import { Link } from "react-router-dom";

const Privacy = () => {
  return (
    <div className="min-h-screen bg-charcoal text-white">
      <main className="container mx-auto px-6 py-12 max-w-4xl">
        <div className="mb-8">
          <Link to="/" className="text-primary hover:text-primary/80 transition-colors text-sm mb-4 inline-block">
            ← Back to Home
          </Link>
          <h1 className="text-4xl font-bold mb-4">Privacy Policy</h1>
          <p className="text-off-white/70">Last Updated: {new Date().toLocaleDateString()}</p>
        </div>

        <div className="prose prose-invert max-w-none space-y-8">
          <section>
            <h2 className="text-2xl font-semibold mb-4">1. Introduction</h2>
            <p className="text-off-white/90 leading-relaxed">
              Omnara ("we," "our," or "us") is committed to protecting your privacy. This Privacy Policy explains how we collect, use, disclose, and safeguard your information when you use our mobile application and services (collectively, the "Service").
            </p>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">2. Information We Collect</h2>
            
            <h3 className="text-xl font-semibold mb-3">Personal Information</h3>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>Account Information: Email address, name, and profile information you provide during registration</li>
              <li>Payment Information: Processed securely through Apple App Store/Google Play Store (we do not store payment details)</li>
              <li>Usage Data: Information about how you interact with AI agents and use our Service</li>
              <li>Message Content: Your conversations with AI agents are securely stored to enable seamless access across all your devices</li>
            </ul>

            <h3 className="text-xl font-semibold mb-3 mt-4">Automatically Collected Information</h3>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>Device Information: Device type, operating system, unique device identifiers</li>
              <li>Log Data: IP address, access times, app features accessed, and crashes</li>
              <li>Analytics: App performance metrics and usage patterns</li>
            </ul>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">3. How We Use Your Information</h2>
            <p className="text-off-white/90 mb-3">We use the information we collect to:</p>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>Provide, maintain, and improve our Service</li>
              <li>Process transactions and manage subscriptions</li>
              <li>Facilitate communication between you and your AI agents</li>
              <li>Send notifications about AI agent activities</li>
              <li>Respond to customer service requests</li>
              <li>Monitor and analyze usage patterns to improve user experience</li>
              <li>Comply with legal obligations</li>
            </ul>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">4. Data Sharing and Disclosure</h2>
            <p className="text-off-white/90 mb-3">
              We do not sell, trade, or rent your personal information. We may share information:
            </p>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>Limited account information with service providers who assist in operating our Service (e.g., authentication, analytics)</li>
              <li>Any information as required to comply with legal obligations or respond to lawful requests</li>
              <li>Information necessary to protect our rights, privacy, safety, or property</li>
              <li>With your explicit consent or at your direction</li>
            </ul>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">5. Your Data Privacy</h2>
            <p className="text-off-white/90 mb-3">We respect your privacy and handle your data with care:</p>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>Your conversations are securely stored to sync across your devices and maintain your chat history</li>
              <li>We do not access or review your messages unless you specifically request support, or we are required to do so for legal compliance</li>
              <li>We never share your message content with third parties</li>
              <li>You have full control - delete any messages or conversations at any time, and they're immediately and permanently removed from our systems</li>
            </ul>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">6. Data Security</h2>
            <p className="text-off-white/90 leading-relaxed">
              We implement appropriate technical and organizational security measures to protect your personal information against unauthorized access, alteration, disclosure, or destruction. However, no method of transmission over the Internet or electronic storage is 100% secure. We cannot guarantee absolute security of your data and are not liable for any data breach, unauthorized access, or security incident beyond our reasonable control. You use the Service at your own risk.
            </p>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">7. Data Retention</h2>
            <p className="text-off-white/90 leading-relaxed">
              We retain your personal information for as long as necessary to provide our Service and fulfill the purposes outlined in this Privacy Policy, unless a longer retention period is required by law. Message content that you delete is immediately and permanently removed from our servers.
            </p>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">8. Your Rights</h2>
            <p className="text-off-white/90 mb-3">You have the right to:</p>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>Access and receive a copy of your personal information</li>
              <li>Correct inaccurate or incomplete information</li>
              <li>Request deletion of your personal information</li>
              <li>Object to or restrict processing of your information</li>
              <li>Data portability where applicable</li>
              <li>Withdraw consent where processing is based on consent</li>
            </ul>
            <p className="text-off-white/90 mt-3">
              To exercise these rights, contact us at contact@omnara.com
            </p>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">9. Third-Party Services</h2>
            <p className="text-off-white/90 leading-relaxed">
              Our Service may contain links to third-party websites or services. We are not responsible for the privacy practices of these third parties. We encourage you to review their privacy policies.
            </p>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">10. Subscription and Payment Processing</h2>
            <ul className="list-disc pl-6 space-y-2 text-off-white/90">
              <li>iOS: Payments are processed by Apple through the App Store</li>
              <li>Android: Payments are processed by Google through Google Play Store</li>
              <li>Web: Payments are processed by Stripe</li>
              <li>We do not have access to your payment card details</li>
              <li>Mobile subscription management is handled through your app store account</li>
              <li>Web subscription management is handled through your account settings</li>
            </ul>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">11. Push Notifications</h2>
            <p className="text-off-white/90 leading-relaxed">
              With your consent, we may send push notifications about AI agent activities, updates, and important service information. You can disable these in your device settings.
            </p>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">12. Changes to This Privacy Policy</h2>
            <p className="text-off-white/90 leading-relaxed">
              We may update this Privacy Policy from time to time. We will notify you of changes by posting the new Privacy Policy in the app and updating the "Last Updated" date.
            </p>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">13. Contact Us</h2>
            <p className="text-off-white/90 leading-relaxed">
              If you have questions about this Privacy Policy or our privacy practices, please contact us at:
            </p>
            <div className="mt-3 text-off-white/90">
              <p>Email: contact@omnara.com</p>
              <p>Website: https://omnara.com</p>
            </div>
          </section>

          <section>
            <h2 className="text-2xl font-semibold mb-4">14. Data Protection Inquiries</h2>
            <p className="text-off-white/90 leading-relaxed">
              For data protection inquiries, please contact us at: contact@omnara.com
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
            <Link to="/terms" className="text-off-white/60 hover:text-omnara-gold transition-colors">
              Terms of Use
            </Link>
          </div>
        </div>
      </main>
    </div>
  );
};

export default Privacy;