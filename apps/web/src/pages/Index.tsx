
import { useState, useEffect, useRef } from 'react';
import { Star } from 'lucide-react';
import HeroSection from '../components/landing/HeroSection';
import HowItWorksSection from '../components/landing/HowItWorksSection';
import ThirdSection from '../components/landing/ThirdSection';
import FinalCTASection from '../components/landing/FinalCTASection';

const Index = () => {
  const [starCount, setStarCount] = useState<number | null>(null);
  const [videoError, setVideoError] = useState(false);
  const videoRef = useRef<HTMLVideoElement>(null);

  useEffect(() => {
    // Fetch GitHub star count
    fetch('https://api.github.com/repos/omnara-ai/omnara')
      .then(res => res.json())
      .then(data => {
        if (data.stargazers_count) {
          setStarCount(data.stargazers_count);
        }
      })
      .catch(err => console.error('Failed to fetch star count:', err));
  }, []);

  return (
    <>
      <style>{`
        html {
          scroll-behavior: smooth;
        }
      `}</style>
      
      {/* Hero Section */}
      <div className="min-h-screen bg-background relative">
        {/* GitHub Badge - Top Right */}
        <div className="absolute top-4 right-4 sm:top-6 sm:right-6 z-50">
          <a
            href="https://github.com/omnara-ai/omnara"
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1.5 sm:gap-2 px-3 sm:px-4 py-1.5 sm:py-2 bg-black/60 backdrop-blur-md border border-white/30 rounded-full hover:bg-black/70 hover:border-white/40 transition-all duration-200 group shadow-lg"
          >
            <svg
              className="w-4 h-4 sm:w-5 sm:h-5 text-white"
              fill="currentColor"
              viewBox="0 0 24 24"
              aria-hidden="true"
            >
              <path d="M12 2C6.477 2 2 6.484 2 12.017c0 4.425 2.865 8.18 6.839 9.504.5.092.682-.217.682-.483 0-.237-.008-.868-.013-1.703-2.782.605-3.369-1.343-3.369-1.343-.454-1.158-1.11-1.466-1.11-1.466-.908-.62.069-.608.069-.608 1.003.07 1.531 1.032 1.531 1.032.892 1.53 2.341 1.088 2.91.832.092-.647.35-1.088.636-1.338-2.22-.253-4.555-1.113-4.555-4.951 0-1.093.39-1.988 1.029-2.688-.103-.253-.446-1.272.098-2.65 0 0 .84-.27 2.75 1.026A9.564 9.564 0 0112 6.844c.85.004 1.705.115 2.504.337 1.909-1.296 2.747-1.027 2.747-1.027.546 1.379.202 2.398.1 2.651.64.7 1.028 1.595 1.028 2.688 0 3.848-2.339 4.695-4.566 4.943.359.309.678.92.678 1.855 0 1.338-.012 2.419-.012 2.747 0 .268.18.58.688.482A10.019 10.019 0 0022 12.017C22 6.484 17.522 2 12 2z" />
            </svg>
            <span className="text-xs sm:text-sm text-white font-semibold">GitHub</span>
            {starCount !== null && (
              <>
                <div className="w-px h-3 sm:h-4 bg-white/40" />
                <div className="flex items-center gap-1">
                  <Star className="w-3 h-3 sm:w-4 sm:h-4 text-yellow-400 fill-yellow-400" />
                  <span className="text-xs sm:text-sm text-white font-semibold">
                    {starCount >= 1000 
                      ? `${(starCount / 1000).toFixed(1)}k` 
                      : starCount.toString()}
                  </span>
                </div>
              </>
            )}
          </a>
        </div>

        {/* Fullscreen Video Container */}
        <div className="absolute inset-0 w-full h-full overflow-hidden bg-black">
        {!videoError ? (
          <video
            ref={videoRef}
            preload="metadata"
            autoPlay
            loop
            muted
            playsInline
            className="absolute inset-0 w-full h-full object-cover scale-110"
            style={{
              minWidth: '110%',
              minHeight: '110%',
              width: 'auto',
              height: 'auto',
              left: '50%',
              top: '50%',
              transform: 'translate(-50%, -50%) scale(1.1)'
            }}
            onError={() => {
              console.log('Video failed to load, using fallback');
              setVideoError(true);
            }}
            onLoadedData={() => {
              console.log('Video loaded successfully');
            }}
          >
            <source src="/lovable-uploads/command-center.mp4" type="video/mp4" />
            <source src="/lovable-uploads/command-center.webm" type="video/webm" />
            Your browser does not support the video tag.
          </video>
        ) : (
          <div 
            className="absolute w-full h-full"
            style={{
              position: 'absolute',
              top: 0,
              left: 0,
              width: '100%',
              height: '100%',
              background: 'linear-gradient(135deg, #1a1a2e 0%, #0f0f1e 25%, #16213e 50%, #0f0f1e 75%, #1a1a2e 100%)',
              backgroundSize: '400% 400%',
              animation: 'gradientShift 15s ease infinite'
            }}
          />
        )}
        
        {/* Warm Dark Gradient Overlay */}
        <div 
          className="absolute inset-0"
          style={{
            background: 'linear-gradient(to bottom, rgba(20,15,10,0.75) 0%, rgba(20,15,10,0.45) 50%, rgba(20,15,10,0.25) 100%)'
          }}
        />
        
        {/* Soft Inner Vignette */}
        <div 
          className="absolute inset-0"
          style={{
            background: 'radial-gradient(ellipse at center, transparent 0%, transparent 40%, rgba(20,15,10,0.5) 100%)'
          }}
        />
        
        {/* Bottom Fade to Black - Creates tunnel effect */}
        <div 
          className="absolute inset-0 pointer-events-none"
          style={{
            background: 'linear-gradient(to bottom, transparent 0%, transparent 75%, rgba(0,0,0,0.2) 85%, rgba(0,0,0,0.5) 92%, rgba(0,0,0,0.8) 98%)'
          }}
        />
        
        {/* Raised Logo Panel */}
        <div className="absolute inset-0 flex items-start justify-center pt-8 md:pt-12">
          <HeroSection />
        </div>
        
        {/* Bottom Badges Container - Responsive Layout */}
        <div className="absolute bottom-6 left-0 right-0 px-6 flex flex-col sm:flex-row sm:justify-between gap-4 sm:gap-0">
          {/* Built by Engineers Badge */}
          <div className="flex items-center justify-center sm:justify-start gap-2 opacity-70 hover:opacity-90 transition-opacity">
            <span className="text-xs sm:text-sm text-omnara-cream-text/60" style={{textShadow: '0 2px 8px rgba(0,0,0,0.8)'}}>
              Built by ex-AI engineers from
            </span>
            
            {/* Meta Logo */}
            <div className="relative w-5 h-5 sm:w-6 sm:h-6">
              <svg viewBox="0 0 287.56 191" className="w-full h-full" style={{filter: 'drop-shadow(0 1px 2px rgba(0,0,0,0.5))'}}>
                <path fill="#FFFFFF" d="M31.06,126c0,11,2.41,19.41,5.56,24.51A19,19,0,0,0,53.19,160c8.1,0,15.51-2,29.79-21.76,11.44-15.83,24.92-38,34-52l15.36-23.6c10.67-16.39,23-34.61,37.18-47C181.07,5.6,193.54,0,206.09,0c21.07,0,41.14,12.21,56.5,35.11,16.81,25.08,25,56.67,25,89.27,0,19.38-3.82,33.62-10.32,44.87C271,180.13,258.72,191,238.13,191V160c17.63,0,22-16.2,22-34.74,0-26.42-6.16-55.74-19.73-76.69-9.63-14.86-22.11-23.94-35.84-23.94-14.85,0-26.8,11.2-40.23,31.17-7.14,10.61-14.47,23.54-22.7,38.13l-9.06,16c-18.2,32.27-22.81,39.62-31.91,51.75C84.74,183,71.12,191,53.19,191c-21.27,0-34.72-9.21-43-23.09C3.34,156.6,0,141.76,0,124.85Z"/>
                <path fill="#FFFFFF" d="M24.49,37.3C38.73,15.35,59.28,0,82.85,0c13.65,0,27.22,4,41.39,15.61,15.5,12.65,32,33.48,52.63,67.81l7.39,12.32c17.84,29.72,28,45,33.93,52.22,7.64,9.26,13,12,19.94,12,17.63,0,22-16.2,22-34.74l27.4-.86c0,19.38-3.82,33.62-10.32,44.87C271,180.13,258.72,191,238.13,191c-12.8,0-24.14-2.78-36.68-14.61-9.64-9.08-20.91-25.21-29.58-39.71L146.08,93.6c-12.94-21.62-24.81-37.74-31.68-45C107,40.71,97.51,31.23,82.35,31.23c-12.27,0-22.69,8.61-31.41,21.78Z"/>
                <path fill="#FFFFFF" d="M82.35,31.23c-12.27,0-22.69,8.61-31.41,21.78C38.61,71.62,31.06,99.34,31.06,126c0,11,2.41,19.41,5.56,24.51L10.14,167.91C3.34,156.6,0,141.76,0,124.85,0,94.1,8.44,62.05,24.49,37.3,38.73,15.35,59.28,0,82.85,0Z"/>
              </svg>
            </div>
            
            {/* Microsoft Logo */}
            <div className="relative w-4 h-4 sm:w-5 sm:h-5">
              <svg viewBox="0 0 129 129" className="w-full h-full">
                <path fill="#F25022" d="M0,0h61.3v61.3H0V0z"/>
                <path fill="#7FBA00" d="M67.7,0H129v61.3H67.7V0z"/>
                <path fill="#00A4EF" d="M0,67.7h61.3V129H0V67.7z"/>
                <path fill="#FFB900" d="M67.7,67.7H129V129H67.7V67.7z"/>
              </svg>
            </div>
            
            {/* Amazon Logo */}
            <div className="relative w-5 h-5 sm:w-6 sm:h-6">
              <svg viewBox="0 0 122.879 111.709" className="w-full h-full">
                <g>
                  <path fill="#FFFFFF" d="M33.848,54.85c0-5.139,1.266-9.533,3.798-13.182c2.532-3.649,5.995-6.404,10.389-8.266 c4.021-1.713,8.974-2.941,14.858-3.687c2.01-0.223,5.287-0.521,9.83-0.894v-1.899c0-4.766-0.521-7.968-1.564-9.607 c-1.564-2.235-4.021-3.351-7.373-3.351h-0.893c-2.458,0.223-4.581,1.005-6.368,2.345c-1.787,1.341-2.942,3.202-3.463,5.586 c-0.298,1.489-1.042,2.345-2.234,2.569l-12.847-1.564c-1.266-0.298-1.899-0.968-1.899-2.011c0-0.223,0.037-0.484,0.111-0.781 c1.266-6.628,4.375-11.543,9.328-14.746C50.473,2.161,56.264,0.373,62.893,0h2.793c8.488,0,15.117,2.197,19.885,6.591 c0.746,0.748,1.438,1.55,2.066,2.401c0.631,0.856,1.135,1.62,1.506,2.29c0.373,0.67,0.709,1.639,1.006,2.904 c0.299,1.267,0.521,2.142,0.672,2.625c0.148,0.484,0.26,1.527,0.334,3.129c0.074,1.601,0.111,2.55,0.111,2.848v27.034 c0,1.936,0.279,3.705,0.838,5.306c0.559,1.602,1.1,2.756,1.619,3.463c0.521,0.707,1.379,1.844,2.57,3.406 c0.447,0.672,0.67,1.268,0.67,1.789c0,0.596-0.297,1.115-0.895,1.563c-6.18,5.363-9.531,8.268-10.053,8.715 c-0.893,0.67-1.973,0.744-3.24,0.223c-1.041-0.895-1.953-1.75-2.736-2.57c-0.781-0.818-1.34-1.414-1.676-1.787 c-0.334-0.371-0.875-1.098-1.619-2.178s-1.268-1.807-1.564-2.178c-4.17,4.543-8.266,7.373-12.287,8.49 c-2.533,0.744-5.661,1.117-9.384,1.117c-5.735,0-10.445-1.77-14.131-5.307C35.691,66.336,33.848,61.328,33.848,54.85L33.848,54.85z M53.062,52.615c0,2.905,0.727,5.232,2.178,6.982c1.453,1.75,3.407,2.625,5.865,2.625c0.224,0,0.54-0.037,0.95-0.111 c0.408-0.076,0.688-0.113,0.838-0.113c3.127-0.818,5.547-2.828,7.26-6.031c0.82-1.415,1.434-2.96,1.844-4.636 c0.41-1.675,0.633-3.035,0.67-4.078c0.037-1.042,0.057-2.755,0.057-5.138v-2.793c-4.32,0-7.596,0.298-9.83,0.894 C56.338,42.077,53.062,46.21,53.062,52.615L53.062,52.615z"/>
                  <path fill="#FF9900" d="M99.979,88.586c0.15-0.299,0.373-0.596,0.672-0.895c1.861-1.266,3.648-2.121,5.361-2.568 c2.83-0.744,5.586-1.154,8.266-1.229c0.746-0.076,1.453-0.037,2.123,0.111c3.352,0.297,5.361,0.857,6.033,1.676 c0.297,0.447,0.445,1.117,0.445,2.01v0.783c0,2.605-0.707,5.678-2.121,9.215c-1.416,3.537-3.389,6.387-5.922,8.547 c-0.371,0.297-0.707,0.445-1.004,0.445c-0.15,0-0.299-0.037-0.447-0.111c-0.447-0.223-0.559-0.633-0.336-1.229 c2.756-6.479,4.133-10.984,4.133-13.518c0-0.818-0.148-1.414-0.445-1.787c-0.746-0.893-2.83-1.34-6.256-1.34 c-1.268,0-2.756,0.074-4.469,0.223c-1.861,0.225-3.574,0.447-5.139,0.672c-0.447,0-0.744-0.076-0.895-0.225 c-0.148-0.148-0.186-0.297-0.111-0.447C99.867,88.846,99.904,88.734,99.979,88.586L99.979,88.586z M0.223,86.688 c0.373-0.596,0.968-0.633,1.788-0.113c18.618,10.799,38.875,16.199,60.769,16.199c14.598,0,29.008-2.719,43.232-8.156 c0.371-0.148,0.912-0.371,1.619-0.67c0.709-0.297,1.211-0.521,1.508-0.67c1.117-0.447,1.992-0.223,2.625,0.67 c0.635,0.895,0.43,1.713-0.613,2.457c-1.342,0.969-3.055,2.086-5.139,3.352c-6.404,3.799-13.555,6.74-21.449,8.826 c-7.893,2.086-15.602,3.127-23.123,3.127c-11.618,0-22.603-2.029-32.954-6.088C18.134,101.563,8.862,95.846,0.67,88.475 C0.223,88.102,0,87.729,0,87.357C0,87.133,0.074,86.91,0.223,86.688L0.223,86.688z"/>
                </g>
              </svg>
            </div>
          </div>
          
          {/* Backed by Y Combinator Badge */}
          <div className="flex items-center justify-center sm:justify-end gap-2 opacity-70 hover:opacity-90 transition-opacity">
            <span className="text-xs sm:text-sm text-omnara-cream-text/60 text-shadow-md">
              Backed by
            </span>
            <div className="w-4 h-4 sm:w-5 sm:h-5 bg-gradient-to-r from-orange-400 to-orange-500 rounded-sm flex items-center justify-center">
              <span className="text-white font-bold" style={{fontSize: '10px'}}>Y</span>
            </div>
            <span className="text-xs sm:text-sm font-medium text-orange-400 text-shadow-md">
              Combinator
            </span>
          </div>
        </div>
        
        </div>
      </div>
      
      {/* How It Works Section */}
      <HowItWorksSection />
      
      {/* Third Section */}
      <ThirdSection />
      
      {/* Final CTA Section */}
      <FinalCTASection />
    </>
  );
};

export default Index;
