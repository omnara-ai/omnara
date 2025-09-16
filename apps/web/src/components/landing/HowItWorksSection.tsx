const HowItWorksSection = () => {
  return (
    <section className="relative min-h-screen bg-black overflow-hidden">
      <style>{`
        .retro-title {
          font-family: 'Press Start 2P', cursive;
          font-size: 1.5rem;
          letter-spacing: 0.08em;
          position: relative;
          display: inline-block;
          color: hsl(var(--omnara-cream-text));
          text-transform: uppercase;
          filter: drop-shadow(0 2px 8px rgba(0,0,0,0.8));
        }
        
        .retro-title::before {
          content: 'AI IN YOUR POCKET';
          position: absolute;
          left: 0;
          top: 0;
          color: #2a2a2a;
          z-index: -1;
          transform: translate(3px, 3px);
        }
        
        .retro-title::after {
          content: 'AI IN YOUR POCKET';
          position: absolute;
          left: 0;
          top: 0;
          color: #1a1a1a;
          z-index: -2;
          transform: translate(6px, 6px);
        }
        
        @media (max-width: 640px) {
          .retro-title {
            font-size: 1.25rem;
            letter-spacing: 0.05em;
          }
          .retro-title::before {
            transform: translate(2px, 2px);
          }
          .retro-title::after {
            transform: translate(4px, 4px);
          }
        }
        
        @media (min-width: 768px) {
          .retro-title {
            font-size: 2.5rem;
            letter-spacing: 0.1em;
          }
          .retro-title::before {
            transform: translate(4px, 4px);
          }
          .retro-title::after {
            transform: translate(8px, 8px);
          }
        }
        
        @media (min-width: 1024px) {
          .retro-title {
            font-size: 3.125rem;
          }
        }
      `}</style>
      
      {/* Fantasy Background Image */}
      <picture className="absolute inset-0 w-full h-full">
        <source type="image/avif" srcSet="/uploads/backgrounds/phone_bg_2/phone_bg_2.avif" />
        <source type="image/webp" srcSet="/uploads/backgrounds/phone_bg_2/phone_bg_2.webp" />
        <img
          src="/uploads/backgrounds/phone_bg_2/phone_bg_2.jpg"
          alt="Fantasy landscape background"
          className="absolute inset-0 w-full h-full object-cover"
          decoding="async"
        />
      </picture>
      
      {/* Dark overlay for overall tinting */}
      <div 
        className="absolute inset-0"
        style={{
          backgroundColor: 'rgba(20, 20, 40, 0.4)', // Dark navy tint
        }}
      />
      
      {/* Top gradient fade - stronger black for tunnel effect */}
      <div 
        className="absolute inset-0"
        style={{
          background: 'linear-gradient(to bottom, #000000 0%, rgba(0, 0, 0, 0.7) 5%, rgba(0, 0, 0, 0.4) 15%, rgba(0, 0, 0, 0.2) 25%, transparent 35%)'
        }}
      />
      
      {/* Additional subtle vignette for depth */}
      <div 
        className="absolute inset-0"
        style={{
          background: 'radial-gradient(ellipse at center, transparent 0%, rgba(0, 0, 0, 0.2) 70%, rgba(0, 0, 0, 0.4) 100%)'
        }}
      />
      
      {/* Bottom Fade to Black - Creates tunnel effect to next section */}
      <div 
        className="absolute inset-0 pointer-events-none"
        style={{
          background: 'linear-gradient(to bottom, transparent 0%, transparent 75%, rgba(0,0,0,0.2) 85%, rgba(0,0,0,0.5) 92%, rgba(0,0,0,0.8) 98%)'
        }}
      />
      
      {/* Section Title */}
      <div className="relative z-10 pt-16 md:pt-20 lg:pt-24 text-center">
        <h2 className="retro-title">
          AI in Your Pocket
        </h2>
      </div>
      
      {/* Phone Screens Container */}
      <div className="relative z-10 mt-12 md:mt-16 lg:mt-20 px-4 sm:px-6">
        <div className="flex flex-col md:flex-row items-center md:items-start justify-center gap-10 md:gap-16 lg:gap-20 max-w-7xl mx-auto">
          {/* Phone 1 - Launch Agent */}
          <div className="flex flex-col items-center">
            <div className="relative transform hover:scale-105 transition-transform duration-300">
              <picture>
                <source type="image/avif" srcSet="/uploads/phone-shots/screen-one/screen-one.avif" />
                <source type="image/webp" srcSet="/uploads/phone-shots/screen-one/screen-one.webp" />
                <img
                  src="/uploads/phone-shots/screen-one/screen-one.jpg"
                  alt="Launch agents from mobile"
                  className="w-full max-w-[275px] sm:max-w-[312px] md:max-w-[350px] lg:max-w-[312px] h-auto"
                  style={{
                    filter: 'drop-shadow(0 20px 40px rgba(0,0,0,0.5)) drop-shadow(0 0 20px rgba(139,92,246,0.3))',
                  }}
                  decoding="async"
                />
              </picture>
            </div>
            <div className="mt-6 text-center max-w-[250px]">
              <h3 className="text-white font-semibold text-lg sm:text-xl mb-1">Launch Agent</h3>
              <p className="text-gray-400 text-sm sm:text-base" style={{ textShadow: '0 2px 4px rgba(0,0,0,0.7)' }}>Kick off Claude Code with a simple prompt.</p>
            </div>
          </div>
          
          {/* Phone 2 - Track & Monitor */}
          <div className="flex flex-col items-center">
            <div className="relative transform hover:scale-105 transition-transform duration-300">
              <picture>
                <source type="image/avif" srcSet="/uploads/phone-shots/screen-two/screen-two.avif" />
                <source type="image/webp" srcSet="/uploads/phone-shots/screen-two/screen-two.webp" />
                <img
                  src="/uploads/phone-shots/screen-two/screen-two.jpg"
                  alt="Monitor agent instances"
                  className="w-full max-w-[275px] sm:max-w-[312px] md:max-w-[350px] lg:max-w-[312px] h-auto"
                  style={{
                    filter: 'drop-shadow(0 20px 40px rgba(0,0,0,0.5)) drop-shadow(0 0 20px rgba(139,92,246,0.3))',
                  }}
                  decoding="async"
                />
              </picture>
            </div>
            <div className="mt-6 text-center max-w-[250px]">
              <h3 className="text-white font-semibold text-lg sm:text-xl mb-1">Track & Monitor</h3>
              <p className="text-gray-400 text-sm sm:text-base" style={{ textShadow: '0 2px 4px rgba(0,0,0,0.7)' }}>See all running agents and their status at a glance.</p>
            </div>
          </div>
          
          {/* Phone 3 - Interact in Real-Time */}
          <div className="flex flex-col items-center">
            <div className="relative transform hover:scale-105 transition-transform duration-300">
              <picture>
                <source type="image/avif" srcSet="/uploads/phone-shots/screen-three/screen-three.avif" />
                <source type="image/webp" srcSet="/uploads/phone-shots/screen-three/screen-three.webp" />
                <img
                  src="/uploads/phone-shots/screen-three/screen-three.jpg"
                  alt="Guide agents in real-time"
                  className="w-full max-w-[275px] sm:max-w-[312px] md:max-w-[350px] lg:max-w-[312px] h-auto"
                  style={{
                    filter: 'drop-shadow(0 20px 40px rgba(0,0,0,0.5)) drop-shadow(0 0 20px rgba(139,92,246,0.3))',
                  }}
                  decoding="async"
                />
              </picture>
            </div>
            <div className="mt-6 text-center max-w-[250px]">
              <h3 className="text-white font-semibold text-lg sm:text-xl mb-1">Interact in Real-Time</h3>
              <p className="text-gray-400 text-sm sm:text-base" style={{ textShadow: '0 2px 4px rgba(0,0,0,0.7)' }}>Step in when agents need your guidance.</p>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
};

export default HowItWorksSection;