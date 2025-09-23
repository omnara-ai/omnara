import { useLocation, Link } from "react-router-dom";
import { useEffect } from "react";
import { Button } from "@/components/ui/button";

const NotFound = () => {
  const location = useLocation();

  useEffect(() => {
    console.error(
      "404 Error: User attempted to access non-existent route:",
      location.pathname
    );
    
    // Set proper page title for SEO
    document.title = "404 - Page Not Found | Omnara";
    
    // Add meta description
    const metaDescription = document.querySelector('meta[name="description"]');
    if (metaDescription) {
      metaDescription.setAttribute('content', 'The page you are looking for could not be found. Return to Omnara - The AI Agent Command Center.');
    }
    
    // Set noindex for 404 pages
    const metaRobots = document.createElement('meta');
    metaRobots.name = 'robots';
    metaRobots.content = 'noindex, follow';
    document.head.appendChild(metaRobots);
    
    return () => {
      // Cleanup
      document.head.removeChild(metaRobots);
      // Reset title
      document.title = "Omnara - AI Agent Command Center | Monitor & Manage AI Agents";
    };
  }, [location.pathname]);

  return (
    <div className="min-h-screen bg-midnight-blue flex items-center justify-center">
      <div className="text-center space-y-8 p-8">
        <h1 className="text-8xl font-bold text-electric-accent">404</h1>
        <h2 className="text-4xl font-semibold text-white">Page Not Found</h2>
        <p className="text-xl text-off-white max-w-md mx-auto">
          The page you're looking for doesn't exist or has been moved.
        </p>
        <nav aria-label="404 navigation">
          <Button size="lg" asChild>
            <Link to="/">
              Return Home
            </Link>
          </Button>
        </nav>
        <div className="mt-8 space-y-4">
          <p className="text-off-white/70">Here are some helpful links:</p>
          <div className="flex flex-col sm:flex-row gap-4 justify-center">
            <Link to="/pricing" className="text-electric-accent hover:underline">Pricing</Link>
            <Link to="/dashboard" className="text-electric-accent hover:underline">Dashboard</Link>
            <a href="/#features" className="text-electric-accent hover:underline">Features</a>
            <a href="/#contact" className="text-electric-accent hover:underline">Contact</a>
          </div>
        </div>
      </div>
    </div>
  );
};

export default NotFound;
